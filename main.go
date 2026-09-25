package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

type Request struct {
	Category    string `json:"category"`
	Address     string `json:"address"`
	Description string `json:"description"`
	BuildingID  string `json:"building_id"`
}

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Write([]byte("OK"))
	})

	http.HandleFunc("/categories", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		categories := []string{
			"Протечка",
			"Нет отопления",
			"Не работает лифт",
			"Нет освещения",
			"Другое",
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(categories)
	})

	http.HandleFunc("/buildings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(buildings)
	})

	http.HandleFunc("/requests/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var request Request

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		request.Category = strings.TrimSpace(request.Category)
		request.Description = strings.TrimSpace(request.Description)

		if !isValidCategory(request.Category) {
			http.Error(w, "Unknown category", http.StatusBadRequest)
			return
		}

		if request.Description == "" {
			http.Error(w, "Description is required", http.StatusBadRequest)
			return
		}

		building, found := findBuilding(request.BuildingID)
		if !found {
			http.Error(w, "Building not found", http.StatusBadRequest)
			return
		}

		request.Address = building.Address

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(struct {
			Request     Request `json:"request"`
			ManagingOrg string  `json:"managing_org"`
			Responsible string  `json:"responsible"`
			NextStep    string  `json:"next_step"`
		}{
			Request:     request,
			ManagingOrg: building.ManagingOrg,
			Responsible: responsibleFor(request.Category, building),
			NextStep:    nextStepFor(request.Category),
		})
	})

	http.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(getAllRequests())
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var request Request

		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		request.Category = strings.TrimSpace(request.Category)
		request.Description = strings.TrimSpace(request.Description)

		if !isValidCategory(request.Category) {
			http.Error(w, "Unknown category", http.StatusBadRequest)
			return
		}

		if request.Description == "" {
			http.Error(w, "Description is required", http.StatusBadRequest)
			return
		}

		building, found := findBuilding(request.BuildingID)
		if !found {
			http.Error(w, "Building not found", http.StatusBadRequest)
			return
		}

		request.Address = building.Address
		saved := createRequest(request, building)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(saved)
	})

	http.HandleFunc("/requests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/requests/")
		id = strings.TrimSpace(id)

		if id == "" {
			http.Error(w, "Request ID is required", http.StatusBadRequest)
			return
		}

		saved, found := findRequestByID(id)
		if !found {
			http.Error(w, "Request not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(saved)
	})

	http.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		http.ServeFile(w, r, "web/index.html")
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.Redirect(w, r, "/app", http.StatusSeeOther)
	})
	http.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		http.ServeFile(w, r, "web/admin.html")
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Сервер запущен на порту " + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
