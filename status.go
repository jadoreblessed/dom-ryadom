package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	statusNew      = "Новая"
	statusInWork   = "В работе"
	statusDone     = "Выполнена"
	statusRejected = "Отклонена"
)

// statuses — все статусы, которые знает система. Единственный источник правды:
// и список для интерфейса, и проверка входных данных берутся отсюда.
//
// Таблицы переходов больше нет, и это осознанное изменение. Раньше она
// запрещала уходить из «Выполненой» и «Отклоненной», и это было плохо: ошибку
// диспетчера (поставил «Выполнена» на нерешённой заявке) было нечем
// исправить, и единственным выходом оставалась новая заявка от жителя.
// Теперь любой статус можно поставить на место любого.
//
//	Новая / В работе / Выполнена / Отклонена — состояние заявки «сейчас»,
//	а не запись в её истории. История появится в шаге 4.
//
// ТЗ требует лишь, чтобы статус был определён и виден. Конкретные статусы и
// правила их смены — наше решение, а не требование.
var statuses = []string{statusNew, statusInWork, statusDone, statusRejected}

// closedStatuses — статусы, в которых заявка выглядит закрытой.
//
// Сменить её отсюда можно, но в интерфейсе список переходов спрятан за кнопкой
// «Изменить статус»: вернуть закрытую заявку в работу не должно быть так же
// легко, как взять новую в работу.
var closedStatuses = map[string]bool{
	statusDone:     true,
	statusRejected: true,
}

func isKnownStatus(status string) bool {
	for _, s := range statuses {
		if s == status {
			return true
		}
	}

	return false
}

func isClosed(status string) bool {
	return closedStatuses[status]
}

// closedStatusesList — закрытые статусы в порядке statuses, а не в случайном
// порядке обхода карты: ответ сервера не должен меняться от запроса к запросу.
func closedStatusesList() []string {
	closed := []string{}

	for _, s := range statuses {
		if closedStatuses[s] {
			closed = append(closed, s)
		}
	}

	return closed
}

// canChange — единственный запрет на смену статуса: поставить заявке тот же
// статус, который у неё уже стоит. Это опечатка в интерфейсе, а не решение
// диспетчера, поэтому это 400, а не 409.
func canChange(from, to string) error {
	if from == to {
		return fmt.Errorf("%w: %q", ErrSameStatus, to)
	}

	return nil
}

// validateStatusChange проверяет то, что можно проверить без базы: статус
// существует, и для отказа указана причина.
//
// Причина обязательна только для «Отклонена»: её читает житель, и «отказано»
// без объяснения для него ничего не значит. Возврат из закрытого статуса
// причины не требует — это исправление ошибки, а не решение по заявке.
func validateStatusChange(to, comment string) error {
	if !isKnownStatus(to) {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, to)
	}

	if to == statusRejected && strings.TrimSpace(comment) == "" {
		return ErrReasonRequired
	}

	return nil
}

func changeRequestStatus(ctx context.Context, number, to, actor, comment string) (SavedRequest, error) {
	if !isKnownStatus(to) {
		return SavedRequest{}, fmt.Errorf("%w: %q", ErrUnknownStatus, to)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return SavedRequest{}, err
	}
	defer tx.Rollback(ctx)

	var dbID int64
	var from string

	err = tx.QueryRow(ctx,
		`SELECT id, status FROM requests WHERE number = $1 FOR UPDATE`, number,
	).Scan(&dbID, &from)
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedRequest{}, fmt.Errorf("%w: %s", ErrNotFound, number)
	}
	if err != nil {
		return SavedRequest{}, err
	}

	// Текущий статус известен только здесь, поэтому и проверка «не менять
	// статус на тот же» живёт внутри транзакции.
	if err := canChange(from, to); err != nil {
		return SavedRequest{}, err
	}

	_, err = tx.Exec(ctx, `
		UPDATE requests
		SET status = $2, comment = $3, updated_at = now()
		WHERE id = $1
	`, dbID, to, strings.TrimSpace(comment))
	if err != nil {
		return SavedRequest{}, err
	}

	// Журнал пишется здесь же, в той же транзакции. Если бы мы записали его
	// после COMMIT, запись могла бы не появиться вовсе, и мы бы потеряли ровно
	// то, ради чего журнал существует: факт решения с его причиной.
	if err := recordStatusChange(ctx, tx, dbID, newStatusChange(from, to, actor, comment)); err != nil {
		return SavedRequest{}, err
	}

	saved, err := scanRequests(tx.QueryRow(ctx,
		`SELECT `+requestColumns+requestJoin+`
		WHERE r.id = $1`, dbID,
	))
	if err != nil {
		return SavedRequest{}, err
	}

	if saved.MaxUserID != nil {
		_, err = tx.Exec(ctx, `INSERT INTO max_notifications (user_id, request_number, text)
			VALUES ($1, $2, $3)`, *saved.MaxUserID, saved.Number, statusNotificationText(saved))
		if err != nil {
			return SavedRequest{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SavedRequest{}, err
	}

	return saved, nil
}
