package main

type Building struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	ManagingOrg string `json:"managing_org"`
}

var buildings = []Building{
	{
		ID:          "house-1",
		Address:     "ул. Лесная, д. 5",
		ManagingOrg: "Тестовая УК №1",
	},
	{
		ID:          "house-2",
		Address:     "ул. Садовая, д. 10",
		ManagingOrg: "Тестовая УК №2",
	},
}

func findBuilding(id string) (Building, bool) {
	for _, building := range buildings {
		if building.ID == id {
			return building, true
		}
	}
	return Building{}, false
}
