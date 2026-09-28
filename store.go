package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const requestColumns = `
	r.id, r.number, r.building_id, b.address, m.name,
	r.category, r.description, r.responsible, r.next_step,
	r.status, r.created_at, r.updated_at`

const requestJoin = `
	FROM requests r
	JOIN buildings b ON b.id = r.building_id
	JOIN managing_orgs m ON m.id = b.managing_org_id`

func scanRequests(row pgx.Row) (SavedRequest, error) {
	var s SavedRequest

	err := row.Scan(
		&s.DBID, &s.Number, &s.Request.BuildingID, &s.Request.Address,
		&s.ManagingOrg, &s.Request.Category, &s.Request.Description,
		&s.Responsible, &s.NextStep, &s.Status, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return SavedRequest{}, err
	}

	s.ID = s.Number

	return s, nil

}

func createRequest(ctx context.Context, building Building, r Request) (SavedRequest, error) {
	saved := SavedRequest{
		Request:     r,
		ManagingOrg: building.ManagingOrg,
		Responsible: responsibleFor(r.Category, building),
		NextStep:    nextStepFor(r.Category),
	}

	err := pool.QueryRow(ctx, `
		INSERT INTO requests (building_id, category, description, responsible, next_step)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, number, status, created_at, updated_at
	`,
		building.ID, r.Category, r.Description, saved.Responsible, saved.NextStep,
	).Scan(&saved.DBID, &saved.Number, &saved.Status, &saved.CreatedAt, &saved.UpdatedAt)
	if err != nil {
		return SavedRequest{}, err
	}

	saved.ID = saved.Number

	return saved, nil
}

func getAllRequests(ctx context.Context, limit int) ([]SavedRequest, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+requestColumns+requestJoin+`
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $1`, limit)
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
