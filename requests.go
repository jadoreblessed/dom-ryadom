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
	MaxUserID   *int64    `json:"-"`
	ID          string    `json:"id"`
	Number      string    `json:"number"`
	Request     Request   `json:"request"`
	ManagingOrg string    `json:"managing_org"`
	Responsible string    `json:"responsible"`
	NextStep    string    `json:"next_step"`
	Status      string    `json:"status"`
	Comment     string    `json:"comment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// StatusChange — одна строка журнала: как заявка попала из одного статуса в
// другой. Отдельный тип, а не поля в SavedRequest, потому что история — это
// список, а у заявки статус один.
type StatusChange struct {
	From    string    `json:"from_status"`
	To      string    `json:"to_status"`
	By      string    `json:"changed_by"`
	Comment string    `json:"comment"`
	At      time.Time `json:"changed_at"`
}
