package main

import (
	"fmt"
	"sync"
	"time"
)

type SavedRequest struct {
	ID          string    `json:"id"`
	Request     Request   `json:"request"`
	ManagingOrg string    `json:"managing_org"`
	Responsible string    `json:"responsible"`
	NextStep    string    `json:"next_step"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	requests      []SavedRequest
	requestsMutex sync.Mutex
	requestNumber int
)

func createRequest(request Request, building Building) SavedRequest {
	requestsMutex.Lock()
	defer requestsMutex.Unlock()

	requestNumber++

	saved := SavedRequest{
		ID:          fmt.Sprintf("REQ-%06d", requestNumber),
		Request:     request,
		ManagingOrg: building.ManagingOrg,
		Responsible: responsibleFor(request.Category, building),
		NextStep:    nextStepFor(request.Category),
		Status:      "Создана",
		CreatedAt:   time.Now(),
	}

	requests = append(requests, saved)

	return saved
}
func findRequestByID(id string) (SavedRequest, bool) {
	requestsMutex.Lock()
	defer requestsMutex.Unlock()

	for _, saved := range requests {
		if saved.ID == id {
			return saved, true
		}
	}

	return SavedRequest{}, false
}
func getAllRequests() []SavedRequest {
	requestsMutex.Lock()
	defer requestsMutex.Unlock()

	result := make([]SavedRequest, len(requests))
	copy(result, requests)

	return result
}
