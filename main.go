package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	requestsLimit   = 200
	maxDescription  = 2000
	shutdownTimeout = 10 * time.Second
)

func main() {
	loadEnv(".env")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := initDB(ctx); err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              ":" + listenPort(),
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Println("Останавливаю сервер…")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("принудительная остановка: %v", err)
		}
	}()

	log.Printf("Сервер запущен на порту %s", srv.Addr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}

	stop()
	log.Println("Сервер остановлен")
}

func listenPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return "8080"
}

func newRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /categories", handleCategories)
	mux.HandleFunc("GET /buildings", handleBuildings)
	mux.HandleFunc("POST /requests/preview", handleRequestPreview)
	mux.HandleFunc("GET /requests", handleRequestList)
	mux.HandleFunc("POST /requests", handleRequestCreate)
	mux.HandleFunc("GET /requests/{number}", handleRequestByNumber)
	mux.HandleFunc("GET /app", handleApp)
	mux.HandleFunc("GET /admin", handleAdmin)
	mux.HandleFunc("GET /{$}", handleRoot)

	return mux
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []string{
		"Протечка",
		"Нет отопления",
		"Не работает лифт",
		"Нет освещения",
		"Другое",
	})
}

func handleBuildings(w http.ResponseWriter, r *http.Request) {
	buildings, err := getAllBuildings(r.Context())
	if err != nil {
		log.Printf("GET /buildings: %v", err)
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить список домов")
		return
	}

	writeJSON(w, http.StatusOK, buildings)
}

type previewResponse struct {
	Request     Request `json:"request"`
	ManagingOrg string  `json:"managing_org"`
	Responsible string  `json:"responsible"`
	NextStep    string  `json:"next_step"`
}

func handleRequestPreview(w http.ResponseWriter, r *http.Request) {
	req, building, ok := decodeRequest(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, previewResponse{
		Request:     req,
		ManagingOrg: building.ManagingOrg,
		Responsible: responsibleFor(req.Category, building),
		NextStep:    nextStepFor(req.Category),
	})
}

func handleRequestList(w http.ResponseWriter, r *http.Request) {
	requests, err := getAllRequests(r.Context(), requestsLimit)
	if err != nil {
		log.Printf("GET /requests: %v", err)
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить список заявок")
		return
	}

	writeJSON(w, http.StatusOK, requests)
}

func handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	req, building, ok := decodeRequest(w, r)
	if !ok {
		return
	}

	saved, err := createRequest(r.Context(), building, req)
	if err != nil {
		log.Printf("POST /requests: %v", err)
		writeError(w, http.StatusInternalServerError, "Не удалось сохранить заявку")
		return
	}

	writeJSON(w, http.StatusCreated, saved)
}

func handleRequestByNumber(w http.ResponseWriter, r *http.Request) {
	number := strings.TrimSpace(r.PathValue("number"))
	if number == "" {
		writeError(w, http.StatusBadRequest, "Не указан номер заявки")
		return
	}

	saved, err := findRequestByNumber(r.Context(), number)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Заявка не найдена")
			return
		}

		log.Printf("GET /requests/%s: %v", number, err)
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить заявку")
		return
	}

	writeJSON(w, http.StatusOK, saved)
}

func handleApp(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/admin.html")
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

func decodeRequest(w http.ResponseWriter, r *http.Request) (Request, Building, bool) {
	var req Request

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDescription+1024))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный формат запроса")
		return Request{}, Building{}, false
	}

	req.Category = strings.TrimSpace(req.Category)
	req.Description = strings.TrimSpace(req.Description)
	req.BuildingID = strings.TrimSpace(req.BuildingID)

	if !isValidCategory(req.Category) {
		writeError(w, http.StatusBadRequest, "Неизвестная категория проблемы")
		return Request{}, Building{}, false
	}

	if req.Description == "" {
		writeError(w, http.StatusBadRequest, "Опишите проблему")
		return Request{}, Building{}, false
	}

	if len([]rune(req.Description)) > maxDescription {
		writeError(w, http.StatusBadRequest, "Описание слишком длинное")
		return Request{}, Building{}, false
	}

	if req.BuildingID == "" {
		writeError(w, http.StatusBadRequest, "Выберите дом")
		return Request{}, Building{}, false
	}

	building, err := findBuilding(r.Context(), req.BuildingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "Дом не найден")
			return Request{}, Building{}, false
		}

		log.Printf("поиск дома %s: %v", req.BuildingID, err)
		writeError(w, http.StatusInternalServerError, "Не удалось найти дом")
		return Request{}, Building{}, false
	}

	req.Address = building.Address

	return req, building, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("не удалось закодировать ответ: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
