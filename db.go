package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func initDB(ctx context.Context) error {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is not set")
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return err
	}

	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour

	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}

	for attempt := 0; attempt < 15; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)

		err = pool.Ping(pingCtx)

		cancel()

		if err == nil {
			// PostgreSQL init scripts run only for a fresh volume. This migration is
			// idempotent, so an existing installation receives the new column too.
			_, err = pool.Exec(ctx, `ALTER TABLE requests ADD COLUMN IF NOT EXISTS max_user_id BIGINT`)
			if err != nil {
				return fmt.Errorf("MAX migration column: %w", err)
			}
			_, err = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS requests_max_user_id_idx
				ON requests (max_user_id) WHERE max_user_id IS NOT NULL`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS max_dialogs (
				user_id BIGINT PRIMARY KEY,
				stage SMALLINT NOT NULL,
				category TEXT NOT NULL DEFAULT '',
				building_id TEXT NOT NULL DEFAULT '',
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
			if err != nil {
				return fmt.Errorf("MAX dialogs migration: %w", err)
			}
			_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS max_notifications (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL,
				request_number TEXT NOT NULL,
				text TEXT NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				delivered_at TIMESTAMPTZ)`)
			if err != nil {
				return fmt.Errorf("MAX notifications migration: %w", err)
			}
			_, err = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS max_notifications_pending_idx
				ON max_notifications (next_attempt_at, id) WHERE delivered_at IS NULL`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS max_poll_cursor (
				id SMALLINT PRIMARY KEY CHECK (id = 1), marker BIGINT)`)
			if err != nil {
				return fmt.Errorf("MAX cursor migration: %w", err)
			}
			_, err = pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS max_inbox (
				id BIGSERIAL PRIMARY KEY,
				event_key TEXT NOT NULL UNIQUE,
				payload JSONB NOT NULL,
				attempts INTEGER NOT NULL DEFAULT 0,
				next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				processed_at TIMESTAMPTZ)`)
			if err != nil {
				return fmt.Errorf("MAX inbox migration: %w", err)
			}
			_, err = pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS max_inbox_pending_idx
				ON max_inbox (id) WHERE processed_at IS NULL`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `ALTER TABLE requests ADD COLUMN IF NOT EXISTS max_message_id TEXT`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS requests_max_message_id_idx
				ON requests (max_message_id)`)
			if err != nil {
				return err
			}
			_, err = pool.Exec(ctx, `ALTER TABLE max_dialogs
				ADD COLUMN IF NOT EXISTS last_message_id TEXT NOT NULL DEFAULT ''`)
			return err
		}

		time.Sleep(time.Second)
	}

	return err
}
