package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MAX retries any non-200 response. Acknowledgement happens only after the
// inbox row is processed, so a sleeping web site cannot strand an event.
func handleMaxWebhook(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimSpace(os.Getenv("MAX_WEBHOOK_SECRET"))
	if activeMaxBot == nil || secret == "" {
		http.Error(w, "webhook unavailable", http.StatusServiceUnavailable)
		return
	}
	provided := r.Header.Get("X-Max-Bot-Api-Secret")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	defer r.Body.Close()
	var update maxUpdate
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&update); err != nil || update.UpdateType == "" {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := processMaxWebhook(ctx, activeMaxBot, update); err != nil {
		log.Printf("MAX webhook: %v", err)
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func processMaxWebhook(ctx context.Context, bot *maxBot, update maxUpdate) error {
	key, _, err := maxEventKey(update)
	if err != nil {
		return err
	}
	if err := storeMaxUpdates(ctx, []maxUpdate{update}, nil); err != nil {
		return err
	}
	for i := 0; i < 32; i++ {
		var done bool
		err := pool.QueryRow(ctx, `SELECT processed_at IS NOT NULL FROM max_inbox WHERE event_key = $1`, key).Scan(&done)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("MAX inbox event missing")
			}
			return err
		}
		if done {
			return nil
		}
		processed, err := bot.processNextInbox(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return errors.New("MAX inbox event pending retry")
		}
	}
	return errors.New("MAX inbox backlog")
}
