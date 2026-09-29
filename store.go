package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const requestColumns = `
	r.id, r.number, r.building_id, b.address, m.name,
	r.category, r.description, r.responsible, r.next_step,
	r.status, r.comment, r.created_at, r.updated_at, r.max_user_id`

const requestJoin = `
	FROM requests r
	JOIN buildings b ON b.id = r.building_id
	JOIN managing_orgs m ON m.id = b.managing_org_id`

func scanRequests(row pgx.Row) (SavedRequest, error) {
	var s SavedRequest

	err := row.Scan(
		&s.DBID, &s.Number, &s.Request.BuildingID, &s.Request.Address,
		&s.ManagingOrg, &s.Request.Category, &s.Request.Description,
		&s.Responsible, &s.NextStep, &s.Status, &s.Comment,
		&s.CreatedAt, &s.UpdatedAt, &s.MaxUserID,
	)
	if err != nil {
		return SavedRequest{}, err
	}

	s.ID = s.Number

	return s, nil

}

// createRequest сохраняет заявку и первую строку её журнала.
//
// Транзакция нужна ради одной цели: не может существовать заявка без записи о
// её появлении. Иначе в журнале была бы дыра в начале — а именно по началу
// судят, что заявка вообще была, когда её открыли в первый раз.
func createRequest(ctx context.Context, building Building, r Request) (SavedRequest, error) {
	return createRequestWithMaxUser(ctx, building, r, nil)
}

// createRequestForMax сохраняет связь заявки с диалогом жителя в MAX. Эта
// связь не попадает в публичный JSON, но нужна для уведомлений о статусе.
func createRequestForMax(ctx context.Context, building Building, r Request, userID int64) (SavedRequest, error) {
	return createRequestWithMaxUser(ctx, building, r, &userID)
}

func createRequestWithMaxUser(ctx context.Context, building Building, r Request, maxUserID *int64) (SavedRequest, error) {
	saved := SavedRequest{
		Request:     r,
		ManagingOrg: building.ManagingOrg,
		Responsible: responsibleFor(r.Category, building),
		NextStep:    nextStepFor(r.Category),
		MaxUserID:   maxUserID,
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return SavedRequest{}, err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO requests (building_id, category, description, responsible, next_step, max_user_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, number, status, created_at, updated_at
	`,
		building.ID, r.Category, r.Description, saved.Responsible, saved.NextStep, maxUserID,
	).Scan(&saved.DBID, &saved.Number, &saved.Status, &saved.CreatedAt, &saved.UpdatedAt)
	if err != nil {
		return SavedRequest{}, err
	}

	// Первая запись журнала: прежнего статуса нет, поэтому from пустой.
	// Автор — сам житель, а не диспетчер.
	if err := recordStatusChange(ctx, tx, saved.DBID,
		newStatusChange("", saved.Status, residentActor, "")); err != nil {
		return SavedRequest{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SavedRequest{}, err
	}

	saved.ID = saved.Number

	return saved, nil
}

// listRequests возвращает заявки с сортировкой и фильтрами.
//
// Фильтры не собираются склейкой строк: все условия стоят на месте всегда, а
// «пустое» значение ($2, $3) означает «не фильтровать». Единственное, что
// подставляется текстом, — колонка сортировки, и только из белого списка
// requestSortColumns.
func listRequests(ctx context.Context, filter RequestFilter) ([]SavedRequest, error) {
	search := ""
	if filter.Search != "" {
		search = "%" + escapeLike(filter.Search) + "%"
	}

	// r.id в конце — чтобы порядок не «прыгал» между двумя заявками с
	// одинаковым значением сортируемой колонки.
	query := `
		SELECT ` + requestColumns + requestJoin + `
		WHERE ($2 = '' OR r.status = $2)
		  AND ($3 = '' OR r.number ILIKE $3
		                    OR b.address ILIKE $3
		                    OR r.description ILIKE $3
		                    OR r.category ILIKE $3)
		ORDER BY ` + filter.sortSQL() + ` ` + filter.orderSQL() + `, r.id DESC
		LIMIT $1`

	rows, err := pool.Query(ctx, query, filter.Limit, filter.Status, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []SavedRequest{}

	for rows.Next() {
		saved, err := scanRequests(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, saved)
	}

	return result, rows.Err()
}

func findRequestByNumber(ctx context.Context, number string) (SavedRequest, error) {
	return scanRequests(pool.QueryRow(ctx,
		`SELECT `+requestColumns+requestJoin+`
		WHERE r.number = $1`, number))
}
