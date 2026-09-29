package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMaxBotCreatesRequestFromDialog(t *testing.T) {
	var replies []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/messages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("user_id"); got != "42" {
			t.Errorf("user_id = %q", got)
		}

		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		replies = append(replies, body.Text)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{}}`))
	}))
	defer api.Close()

	building := Building{ID: "house-1", Address: "ул. Лесная, д. 5", ManagingOrg: "Тестовая УК №1"}
	var created Request

	bot := &maxBot{
		token:   "test-token",
		apiURL:  api.URL,
		client:  api.Client(),
		dialogs: make(map[int64]maxDialogState),
		list: func(context.Context) ([]Building, error) {
			return []Building{building}, nil
		},
		find: func(_ context.Context, id string) (Building, error) {
			if id != building.ID {
				t.Fatalf("building id = %q", id)
			}
			return building, nil
		},
		create: func(_ context.Context, gotBuilding Building, request Request) (SavedRequest, error) {
			if gotBuilding != building {
				t.Fatalf("building = %#v", gotBuilding)
			}
			created = request
			return SavedRequest{
				Number:      "REQ-000001",
				Request:     request,
				Responsible: "Аварийно-диспетчерская служба",
				NextStep:    "Сообщите адрес и место протечки.",
				Status:      "Новая",
			}, nil
		},
	}

	ctx := context.Background()
	for _, message := range []string{"/start", "1", "1", "Течёт труба в подъезде"} {
		if err := bot.handleText(ctx, 42, message); err != nil {
			t.Fatalf("handle %q: %v", message, err)
		}
	}

	if created.Category != "Протечка" || created.BuildingID != "house-1" ||
		created.Address != building.Address || created.Description != "Течёт труба в подъезде" {
		t.Fatalf("created request = %#v", created)
	}
	if len(replies) != 4 {
		t.Fatalf("replies count = %d, want 4", len(replies))
	}
	if !strings.Contains(replies[3], "REQ-000001") || !strings.Contains(replies[3], "Аварийно-диспетчерская служба") {
		t.Fatalf("final reply = %q", replies[3])
	}
	if _, ok := bot.dialog(42); ok {
		t.Fatal("dialog was not removed after creating request")
	}
}

func TestMaxBotGetUpdates(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("marker"); got != "7" {
			t.Errorf("marker = %q", got)
		}
		if got := r.URL.Query().Get("types"); got != "message_created,bot_started" {
			t.Errorf("types = %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"updates":[{
				"update_type":"message_created",
				"message":{"sender":{"user_id":42},"body":{"text":"/start"}}
			}],
			"marker":8
		}`))
	}))
	defer api.Close()

	bot := &maxBot{
		token:  "test-token",
		apiURL: api.URL,
		client: &http.Client{Timeout: time.Second},
	}
	marker := int64(7)
	updates, next, err := bot.getUpdates(context.Background(), &marker)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Body.Text != "/start" {
		t.Fatalf("updates = %#v", updates)
	}
	if next == nil || *next != 8 {
		t.Fatalf("next marker = %v", next)
	}
}

func TestChooseCategoryAndBuilding(t *testing.T) {
	if got, ok := chooseCategory("2"); !ok || got != "Нет отопления" {
		t.Fatalf("category = %q, %v", got, ok)
	}
	if got, ok := chooseCategory("протечка"); !ok || got != "Протечка" {
		t.Fatalf("category = %q, %v", got, ok)
	}

	buildings := []Building{{ID: "house-1", Address: "ул. Лесная, д. 5"}}
	if got, ok := chooseBuilding("1", buildings); !ok || got.ID != "house-1" {
		t.Fatalf("building = %#v, %v", got, ok)
	}
}
