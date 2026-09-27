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
			return nil
		}

		time.Sleep(time.Second)
	}

	return err
}
