package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewStatusChangeTrimsInput(t *testing.T) {
	// В журнал не должно попасть то, чего диспетчер не вводил. Пробелы по
	// краям — не причина отказа, а следы копирования из формы.
	change := newStatusChange(
		"  Новая ",
		" Отклонена ",
		"  dispatcher  ",
		"  не входит в зону ответственности  ",
	)

	if change.From != "Новая" {
		t.Errorf("From = %q, want %q", change.From, "Новая")
	}
	if change.To != "Отклонена" {
		t.Errorf("To = %q, want %q", change.To, "Отклонена")
	}
	if change.By != "dispatcher" {
		t.Errorf("By = %q, want %q", change.By, "dispatcher")
	}
	if change.Comment != "не входит в зону ответственности" {
		t.Errorf("Comment = %q, want %q", change.Comment, "не входит в зону ответственности")
	}
}

func TestNewStatusChangeForNewRequestHasNoPreviousStatus(t *testing.T) {
	// У первой записи журнала «прежнего» статуса нет. from остаётся пустой
	// строкой, а не «Нет» или «-»: пустота в JSON читается однозначно.
	change := newStatusChange("", statusNew, residentActor, "")

	if change.From != "" {
		t.Errorf("From = %q, want пустая строка", change.From)
	}
	if change.To != statusNew {
		t.Errorf("To = %q, want %q", change.To, statusNew)
	}
	if change.By != residentActor {
		t.Errorf("By = %q, want %q — заявку создал житель, а не диспетчер", change.By, residentActor)
	}
}

func TestStatusChangeJSONContract(t *testing.T) {
	// Имена полей — это контракт с интерфейсом, и переименовать их молча
	// нельзя: админка печатает cause.status_by и cause.changed_at.
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	data, err := json.Marshal(StatusChange{
		From:    "",
		To:      statusRejected,
		By:      dispatcherActor,
		Comment: "Территория не входит в зону ответственности",
		At:      at,
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	for _, key := range []string{"from_status", "to_status", "changed_by", "comment", "changed_at"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("в JSON нет поля %q: %s", key, data)
		}
	}

	if len(decoded) != 5 {
		t.Errorf("в JSON %d полей, ожидалось 5: лишнее поле легко не заметить", len(decoded))
	}

	// Пустой прежний статус должен уехать пустой строкой, а не null: клиенту
	// не придётся разбирать два типа одного поля.
	if decoded["from_status"] != "" {
		t.Errorf("from_status = %v, want \"\"", decoded["from_status"])
	}
}

func TestNewStatusChangeKeepsReasonEvenWhenStatusLeaves(t *testing.T) {
	// Главная причина, по которой журнал пишется в той же транзакции.
	// Причина отказа остаётся в записи, даже когда заявку вернули в работу и
	// поле comment в самой заявке уже перезаписали.
	rejected := newStatusChange(statusInWork, statusRejected, dispatcherActor, "не наша зона")
	reopened := newStatusChange(statusRejected, statusInWork, dispatcherActor, "")

	if rejected.Comment == "" {
		t.Fatal("причина отказа не записалась в журнал — это ровно то, что журнал обязан сохранить")
	}

	if reopened.Comment != "" {
		t.Errorf("Comment = %q, want пусто: возврат в работу — не решение, причины у него нет", reopened.Comment)
	}

	if rejected.To != statusRejected || reopened.From != statusRejected {
		t.Errorf("связь записей нарушена: %q -> %q, потом %q -> %q",
			rejected.From, rejected.To, reopened.From, reopened.To)
	}
}
