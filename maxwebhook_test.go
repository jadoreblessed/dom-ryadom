package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaxWebhookRejectsMissingOrWrongSecret(t *testing.T) {
	t.Setenv("MAX_WEBHOOK_SECRET", "test-webhook-secret")
	previous := activeMaxBot
	activeMaxBot = &maxBot{}
	t.Cleanup(func() { activeMaxBot = previous })

	for _, provided := range []string{"", "wrong-secret"} {
		req := httptest.NewRequest(http.MethodPost, "/max/webhook", strings.NewReader(`{"update_type":"bot_started"}`))
		req.Header.Set("X-Max-Bot-Api-Secret", provided)
		response := httptest.NewRecorder()
		handleMaxWebhook(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("secret %q: status %d, want 403", provided, response.Code)
		}
	}
}
