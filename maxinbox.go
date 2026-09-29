package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

func maxEventKey(update maxUpdate) (string, []byte, error) {
	payload, err := json.Marshal(update)
	if err != nil {
		return "", nil, err
	}
	if update.UpdateType == "message_created" && update.Message != nil &&
		update.Message.Body != nil && update.Message.Body.Mid != "" {
		return "message:" + update.Message.Body.Mid, payload, nil
	}
	// Other event types do not always carry a message ID. The hash makes
	// repeat delivery of the same payload stable across process restarts.
	digest := sha256.Sum256(payload)
	return "event:" + hex.EncodeToString(digest[:]), payload, nil
}

func readMaxMarker(ctx context.Context) (*int64, error) {
	var value sql.NullInt64
	err := pool.QueryRow(ctx, `SELECT marker FROM max_poll_cursor WHERE id = 1`).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !value.Valid {
		return nil, nil
	}
	return &value.Int64, nil
}

// Commit the complete response to the inbox before acknowledging its marker.
// A failed insert or commit leaves the old marker intact for the next poll.
func storeMaxUpdates(ctx context.Context, updates []maxUpdate, marker *int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, update := range updates {
		key, payload, err := maxEventKey(update)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO max_inbox (event_key, payload)
			VALUES ($1, $2) ON CONFLICT (event_key) DO NOTHING`, key, payload); err != nil {
			return fmt.Errorf("сохранение события %s: %w", key, err)
		}
	}
	if marker != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO max_poll_cursor (id, marker) VALUES (1, $1)
			ON CONFLICT (id) DO UPDATE SET marker = EXCLUDED.marker`, *marker); err != nil {
			return fmt.Errorf("сохранение курсора MAX: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// Process in receive order: a failed dialogue step must be retried before
// later messages from that dialogue. The row lock also excludes another worker.
func (b *maxBot) processNextInbox(ctx context.Context) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var id int64
	var eventKey string
	var payload []byte
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, event_key, payload, attempts FROM max_inbox
		WHERE id = (SELECT min(id) FROM max_inbox WHERE processed_at IS NULL)
		AND next_attempt_at <= now() FOR UPDATE SKIP LOCKED`).Scan(&id, &eventKey, &payload, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var update maxUpdate
	processErr := json.Unmarshal(payload, &update)
	if processErr == nil {
		if update.UpdateType == "message_created" && update.Message != nil &&
			update.Message.Body != nil && update.Message.Body.Mid == "" {
			update.Message.Body.Mid = eventKey
		}
		processCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		processErr = b.handleUpdate(processCtx, update)
		cancel()
	}
	if processErr == nil {
		_, err = tx.Exec(ctx, `UPDATE max_inbox SET processed_at = now() WHERE id = $1`, id)
	} else {
		delaySeconds := 5 << min(attempts, 7)
		_, err = tx.Exec(ctx, `UPDATE max_inbox
			SET attempts = attempts + 1, next_attempt_at = now() + ($2 * interval '1 second')
			WHERE id = $1`, id, delaySeconds)
		log.Printf("MAX: событие %d будет повторено: %v", id, processErr)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (b *maxBot) runInbox(ctx context.Context) {
	for ctx.Err() == nil {
		processed, err := b.processNextInbox(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("MAX: очередь входящих событий: %v", err)
		}
		if processed && err == nil {
			continue
		}
		if !waitContext(ctx, 2*time.Second) {
			return
		}
	}
}
