package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
		create: func(_ context.Context, gotBuilding Building, request Request, userID int64, messageID string) (SavedRequest, error) {
			if userID != 42 {
				t.Fatalf("MAX user id = %d", userID)
			}
			if messageID != "" {
				t.Fatalf("message id = %q", messageID)
			}
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

func TestMaxBotNotifiesOriginalUserOnStatusChange(t *testing.T) {
	var recipient, message string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recipient = r.URL.Query().Get("user_id")
		var body struct{ Text string `json:"text"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode notification: %v", err)
		}
		message = body.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()

	bot := &maxBot{apiURL: api.URL, client: api.Client()}
	userID := int64(42)
	saved := SavedRequest{Number: "REQ-000001", Status: "Отклонена", Comment: "Неверный адрес", MaxUserID: &userID}
	if err := bot.notifyStatusChange(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if recipient != "42" || !strings.Contains(message, saved.Number) ||
		!strings.Contains(message, saved.Status) || !strings.Contains(message, saved.Comment) {
		t.Fatalf("recipient=%q, notification=%q", recipient, message)
	}
}

func TestMaxBotStatusLookupUsesSenderIdentity(t *testing.T) {
	var reply string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string `json:"text"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply = body.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	bot := &maxBot{
		apiURL: api.URL, client: api.Client(), dialogs: make(map[int64]maxDialogState),
		lookup: func(_ context.Context, userID int64, number string) (SavedRequest, error) {
			if userID != 42 || number != "REQ-000001" {
				t.Fatalf("lookup(user=%d, number=%q)", userID, number)
			}
			return SavedRequest{Number: number, Status: "В работе", Request: Request{Address: "ул. Лесная, д. 5"}}, nil
		},
	}
	if err := bot.handleText(context.Background(), 42, "/status req-000001"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "В работе") {
		t.Fatalf("status reply = %q", reply)
	}
}

func TestMaxBotResumesDialogAfterRestart(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	var stored maxDialogState
	store := func(_ context.Context, userID int64, state maxDialogState) error {
		if userID != 42 {
			t.Fatalf("user id = %d", userID)
		}
		stored = state
		return nil
	}
	first := &maxBot{apiURL: api.URL, client: api.Client(), dialogs: make(map[int64]maxDialogState), save: store}
	if err := first.handleText(context.Background(), 42, "/start"); err != nil {
		t.Fatal(err)
	}
	second := &maxBot{
		apiURL: api.URL, client: api.Client(), dialogs: make(map[int64]maxDialogState), save: store,
		load: func(context.Context, int64) (maxDialogState, bool, error) { return stored, true, nil },
		list: func(context.Context) ([]Building, error) {
			return []Building{{ID: "house-1", Address: "ул. Лесная, д. 5"}}, nil
		},
	}
	if err := second.handleText(context.Background(), 42, "1"); err != nil {
		t.Fatal(err)
	}
	if stored.Stage != maxStageBuilding || stored.Category != "Протечка" {
		t.Fatalf("restored state = %#v", stored)
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

func TestMaxEventKeyUsesMessageIDAcrossRetries(t *testing.T) {
	update := maxUpdate{UpdateType: "message_created", Message: &maxMessage{
		Sender: maxUser{UserID: 42}, Body: &maxMessageBody{Mid: "mid-1", Text: "течёт труба"},
	}}
	key, _, err := maxEventKey(update)
	if err != nil || key != "message:mid-1" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	update.Message.Body.Text = "изменённый текст"
	again, _, err := maxEventKey(update)
	if err != nil || again != key {
		t.Fatalf("repeat key=%q err=%v", again, err)
	}
	update.Message.Body.Mid = "mid-2"
	other, _, _ := maxEventKey(update)
	if other == key {
		t.Fatal("different messages shared inbox key")
	}
}

func TestMaxBotRepeatedCreateMessageReturnsExistingRequest(t *testing.T) {
	var reply string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string `json:"text"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply = body.Text
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	bot := &maxBot{
		apiURL: api.URL, client: api.Client(), dialogs: make(map[int64]maxDialogState),
		byMessage: func(_ context.Context, userID int64, mid string) (SavedRequest, error) {
			if userID != 42 || mid != "mid-1" {
				t.Fatalf("lookup user=%d mid=%q", userID, mid)
			}
			return SavedRequest{Number: "REQ-000001", Status: "Новая", Request: Request{Address: "Лесная, 5"}}, nil
		},
		create: func(context.Context, Building, Request, int64, string) (SavedRequest, error) {
			t.Fatal("duplicate message created a second request")
			return SavedRequest{}, nil
		},
	}
	for i := 0; i < 2; i++ {
		if err := bot.handleTextWithMessage(context.Background(), 42, "течёт труба", "mid-1"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reply, "REQ-000001") {
			t.Fatalf("reply=%q", reply)
		}
	}
}

func TestMaxBotRetryDoesNotAdvanceDialogTwice(t *testing.T) {
	var replies []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string `json:"text"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		replies = append(replies, body.Text)
		if len(replies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	bot := &maxBot{apiURL: api.URL, client: api.Client(), dialogs: map[int64]maxDialogState{
		42: {Stage: maxStageCategory},
	}, list: func(context.Context) ([]Building, error) {
		return []Building{{ID: "house-1", Address: "Лесная, 5"}}, nil
	}, byMessage: func(context.Context, int64, string) (SavedRequest, error) {
		return SavedRequest{}, pgx.ErrNoRows
	}}
	if err := bot.handleTextWithMessage(context.Background(), 42, "1", "mid-category"); err == nil {
		t.Fatal("first send unexpectedly succeeded")
	}
	if err := bot.handleTextWithMessage(context.Background(), 42, "1", "mid-category"); err != nil {
		t.Fatal(err)
	}
	state, _ := bot.dialog(42)
	if state.Stage != maxStageBuilding || state.LastMessageID != "mid-category" {
		t.Fatalf("dialog advanced twice: %#v", state)
	}
	if len(replies) != 2 || replies[0] != replies[1] {
		t.Fatalf("retry replies: %#v", replies)
	}
}
