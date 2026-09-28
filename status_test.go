package main

import (
	"errors"
	"testing"
)

func TestIsKnownStatus(t *testing.T) {
	for _, s := range statuses {
		if !isKnownStatus(s) {
			t.Errorf("isKnownStatus(%q) = false, want true", s)
		}
	}

	for _, s := range []string{"Отменено", "Новая ", "", "новая"} {
		if isKnownStatus(s) {
			t.Errorf("isKnownStatus(%q) = true, want false", s)
		}
	}
}

func TestStatusesHaveNoDuplicates(t *testing.T) {
	seen := map[string]bool{}

	for _, s := range statuses {
		if seen[s] {
			t.Errorf("статус %q есть в списке дважды", s)
		}

		seen[s] = true
	}
}

func TestAnyStatusCanChangeToAnyOther(t *testing.T) {
	// Главное свойство модели: запрещённых переходов нет, диспетчер может
	// исправить любой статус. Единственное исключение — тот же статус.
	for _, from := range statuses {
		for _, to := range statuses {
			err := canChange(from, to)

			if from == to {
				if !errors.Is(err, ErrSameStatus) {
					t.Errorf("canChange(%q, %q) = %v, want %v", from, to, err, ErrSameStatus)
				}
				continue
			}

			if err != nil {
				t.Errorf("canChange(%q, %q) = %v, want nil: заявку нечем исправить", from, to, err)
			}
		}
	}
}

func TestClosedStatusesAreKnown(t *testing.T) {
	// Каждый закрытый статус обязан быть в общем списке, иначе isKnownStatus
	// отвергнет статус, который сервер сам же и предлагает.
	closed := closedStatusesList()

	if len(closed) == 0 {
		t.Fatal("закрытых статусов нет — интерфейсу нечего прятать под кнопку")
	}

	for _, s := range closed {
		if !isKnownStatus(s) {
			t.Errorf("закрытый статус %q отсутствует в списке статусов", s)
		}

		if !isClosed(s) {
			t.Errorf("isClosed(%q) = false, want true", s)
		}
	}
}

func TestClosedStatusesListFollowsStatusesOrder(t *testing.T) {
	// Порядок в ответе сервера не должен прыгать между запросами: карту не
	// обходим, идём по statuses.
	closed := closedStatusesList()
	i := 0

	for _, s := range statuses {
		if !isClosed(s) {
			continue
		}

		if closed[i] != s {
			t.Fatalf("closedStatusesList() = %v, ожидался порядок как в statuses", closed)
		}

		i++
	}
}

func TestValidateStatusChange(t *testing.T) {
	cases := []struct {
		name    string
		to      string
		comment string
		wantErr error
	}{
		{name: "в работу без причины", to: statusInWork, comment: "", wantErr: nil},
		{name: "выполнена без причины", to: statusDone, comment: "", wantErr: nil},
		{name: "отказ с причиной", to: statusRejected, comment: "Территория не входит в нашу зону ответственности", wantErr: nil},
		{name: "отказ без причины", to: statusRejected, comment: "", wantErr: ErrReasonRequired},
		{name: "отказ из пробелов", to: statusRejected, comment: "   ", wantErr: ErrReasonRequired},
		{name: "неизвестный статус", to: "Отменено", comment: "", wantErr: ErrUnknownStatus},
		{name: "пустой статус", to: "", comment: "", wantErr: ErrUnknownStatus},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateStatusChange(c.to, c.comment)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("validateStatusChange(%q, %q) = %v, want %v", c.to, c.comment, err, c.wantErr)
			}
		})
	}
}

func TestReasonRequiredOnlyForRejection(t *testing.T) {
	// Возврат из закрытого статуса — исправление ошибки, а не решение по
	// заявке, поэтому причину требовать нельзя. Иначе диспетчер, который
	// поставил «Выполнена» по ошибке, не смог бы это исправить.
	for _, to := range []string{statusNew, statusInWork, statusDone} {
		if err := validateStatusChange(to, ""); err != nil {
			t.Errorf("validateStatusChange(%q, \"\") = %v, want nil", to, err)
		}
	}
}
