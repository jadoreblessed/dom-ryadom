package main

import "context"

type Building struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	ManagingOrg string `json:"managing_org"`
}

func getAllBuildings(ctx context.Context) ([]Building, error) {
	rows, err := pool.Query(ctx, `
		SELECT b.id, b.address, m.name
		FROM buildings b
		JOIN managing_orgs m ON m.id = b.managing_org_id
		ORDER BY b.address`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Building{}

	for rows.Next() {
		var b Building
		if err := rows.Scan(&b.ID, &b.Address, &b.ManagingOrg); err != nil {
			return nil, err
		}
		result = append(result, b)
	}

	return result, rows.Err()
}

func findBuilding(ctx context.Context, id string) (Building, error) {
	var b Building

	err := pool.QueryRow(ctx, `
		SELECT b.id, b.address, m.name
		FROM buildings b
		JOIN managing_orgs m ON m.id = b.managing_org_id
		WHERE b.id = $1`, id,
	).Scan(&b.ID, &b.Address, &b.ManagingOrg)

	return b, err
}
