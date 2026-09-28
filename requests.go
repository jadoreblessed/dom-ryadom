package main

import "time"

type Request struct {
	Category    string `json:"category"`
	Address     string `json:"address"`
	Description string `json:"description"`
	BuildingID  string `json:"building_id"`
}

type SavedRequest struct {
	DBID        int64     `json:"-"`
	ID          string    `json:"id"`
	Number      string    `json:"number"`
	Request     Request   `json:"request"`
	ManagingOrg string    `json:"managing_org"`
	Responsible string    `json:"responsible"`
	NextStep    string    `json:"next_step"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
