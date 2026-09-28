package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Журнал смены статусов. Таблица request_status_history создана миграцией 001,
// но до этого шага в неё никто не писал: у заявки был только текущий статус, и
// прошлые решения исчезали без следа.
//
// Что это даёт:
//
//   - причину отказа нельзя потерять — она остаётся в записи, даже когда
//     заявку вернули в работу и поле comment перезаписалось;
//   - видно, кто и когда закрыл заявку, а потом вернул её: без этого
//     «возвращённая после отказа» заявка выглядит как будто её и не трогали;
//   - когда появится авторизация, changed_by станет логином, и по журналу
//     можно будет разобрать, что делал конкретный диспетчер.
//
// Пишем в ту же транзакцию, что и смену статуса. Иначе возможна запись
// «статус сменился, а в журнале пусто»: ровно тот случай, ради которого журнал
// и нужен, он бы и не появился.

// newStatusChange собирает запись журнала. Отдельная чистая функция, чтобы её
// можно было проверить без базы: в журнал не должно попасть то, чего диспетчер
// не вводил, — пробелы по краям и пустой автор.
//
// Обрезаются и статусы тоже. Запись в журнале остаётся навсегда, и статус с
// пробелом (« Отклонена») потом не совпадёт ни с одним значением из списка:
// в истории появилась бы строчка, которую никто не смог бы выбрать.
func newStatusChange(from, to, actor, comment string) StatusChange {
	return StatusChange{
		From:    strings.TrimSpace(from),
		To:      strings.TrimSpace(to),
		By:      strings.TrimSpace(actor),
		Comment: strings.TrimSpace(comment),
	}
}

// recordStatusChange пишет одну запись журнала. Принимает pgx.Tx, а не пул:
// вызывается только внутри уже открытой транзакции, чтобы запись журнала и
// смена статуса были одной операцией.
func recordStatusChange(ctx context.Context, tx pgx.Tx, requestID int64, change StatusChange) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO request_status_history
			(request_id, from_status, to_status, changed_by, comment, changed_at)
		VALUES ($1, $2, $3, $4, $5, now())
	`, requestID, change.From, change.To, change.By, change.Comment)

	return err
}

// listRequestHistory отдаёт журнал заявки, свежие записи первыми.
//
// COALESCE(from_status, ”) — для самой первой записи, когда заявка только
// появилась и «прежнего» статуса не было. Пустая строка честнее NULL в JSON:
// клиенту не нужно проверять тип.
func listRequestHistory(ctx context.Context, number string) ([]StatusChange, error) {
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(h.from_status, ''), h.to_status, h.changed_by,
		       COALESCE(h.comment, ''), h.changed_at
		FROM request_status_history h
		JOIN requests r ON r.id = h.request_id
		WHERE r.number = $1
		ORDER BY h.changed_at DESC, h.id DESC
	`, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Срез, а не nil: пустой журнал должен отдаться как [], иначе клиенту
	// придётся отличать null от «истории нет».
	history := []StatusChange{}

	for rows.Next() {
		var change StatusChange

		if err := rows.Scan(&change.From, &change.To, &change.By, &change.Comment, &change.At); err != nil {
			return nil, err
		}

		history = append(history, change)
	}

	return history, rows.Err()
}
