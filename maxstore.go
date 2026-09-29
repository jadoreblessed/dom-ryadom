package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only the minimum needed to resume a dialog is stored. Buildings are always
// reread from the current directory so an outdated address is never trusted.
func saveMaxDialog(ctx context.Context, userID int64, state maxDialogState) error {
	_, err := pool.Exec(ctx, `INSERT INTO max_dialogs (user_id, stage, category, building_id, last_message_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id) DO UPDATE SET stage = EXCLUDED.stage,
		category = EXCLUDED.category, building_id = EXCLUDED.building_id,
		last_message_id = EXCLUDED.last_message_id, updated_at = now()`,
		userID, int16(state.Stage), state.Category, state.Building.ID, state.LastMessageID)
	return err
}

func removeMaxDialog(ctx context.Context, userID int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM max_dialogs WHERE user_id = $1`, userID)
	return err
}

func readMaxDialog(ctx context.Context, userID int64) (maxDialogState, bool, error) {
	var state maxDialogState
	var stage int16
	var buildingID string
	err := pool.QueryRow(ctx, `SELECT stage, category, building_id, last_message_id FROM max_dialogs WHERE user_id = $1`, userID).
		Scan(&stage, &state.Category, &buildingID, &state.LastMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return maxDialogState{}, false, nil
	}
	if err != nil {
		return maxDialogState{}, false, err
	}
	state.Stage = maxDialogStage(stage)
	if state.Stage == maxStageBuilding {
		state.Buildings, err = getAllBuildings(ctx)
	} else if state.Stage == maxStageDescription {
		state.Building, err = findBuilding(ctx, buildingID)
	}
	if err != nil {
		return maxDialogState{}, false, err
	}
	return state, true, nil
}

// The database row is locked while sending. This prevents two workers from
// sending the same notification concurrently if the app is scaled later.
func (b *maxBot) deliverNextNotification(ctx context.Context) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var id, userID int64
	var message string
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, user_id, text, attempts FROM max_notifications
		WHERE delivered_at IS NULL AND next_attempt_at <= now()
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &userID, &message, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sendErr := b.sendMessage(sendCtx, userID, message)
	cancel()
	if sendErr == nil {
		_, err = tx.Exec(ctx, `UPDATE max_notifications SET delivered_at = now() WHERE id = $1`, id)
	} else {
		delaySeconds := 5 << min(attempts, 7)
		_, err = tx.Exec(ctx, `UPDATE max_notifications
			SET attempts = attempts + 1, next_attempt_at = now() + ($2 * interval '1 second')
			WHERE id = $1`, id, delaySeconds)
		log.Printf("MAX: уведомление %d будет повторено: %v", id, sendErr)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (b *maxBot) runNotifications(ctx context.Context) {
	for ctx.Err() == nil {
		delivered, err := b.deliverNextNotification(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("MAX: очередь уведомлений: %v", err)
		}
		if delivered && err == nil {
			continue
		}
		if !waitContext(ctx, 3*time.Second) {
			return
		}
	}
}
