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
	maxDescription   = 2000
	maxCommentLength = 500
	shutdownTimeout  = 10 * time.Second

	// dispatcherActor — запасной автор для смены статуса, если запрос пришёл
	// без сессии. Обычно туда попадает логин вошедшего диспетчера.
	dispatcherActor = "dispatcher"

	// residentActor — автор первой записи в журнале: заявку создал житель, а не
	// диспетчер. Пока нет ни телефона, ни входа, иного автора у заявки нет.
	residentActor = "resident"
)

var activeMaxBot *maxBot

func main() {
	loadEnv(".env")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := initDB(ctx); err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	defer pool.Close()

	// Секрет подписи сессий проверяем до старта. Пустой или короткий секрет
	// означает, что cookie можно подделать, и тихо поднимать сервер в таком
	// состоянии нельзя: это выглядело бы как работающая защита, которой нет.
	if secret := authSecret(); len(secret) < minSecretLength {
		log.Fatalf("AUTH_SECRET не задан или короче %d символов: сессию можно подделать, "+
			"сгенерируйте новый (например, openssl rand -hex 32) и запишите в .env",
			minSecretLength)
	}

	if err := ensureDispatcher(ctx); err != nil {
		log.Fatalf("dispatcher setup failed: %v", err)
	}

	if token := strings.TrimSpace(os.Getenv("MAX_BOT_TOKEN")); token != "" {
		activeMaxBot = newMaxBot(token)
		go activeMaxBot.run(ctx)
		go activeMaxBot.runNotifications(ctx)
		go activeMaxBot.runInbox(ctx)
	} else {
		log.Println("MAX_BOT_TOKEN не задан: MAX-бот отключён")
	}

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

	// Открыто: житель подаёт заявку и видит справочники. Закрыто: всё, чем
	// пользуется диспетчер, — список заявок, карточка, журнал, смена статуса.
	// GET /requests без входа отдавал все заявки всех жителей с адресами и
	// описаниями кому угодно, кто дотянется до порта.
	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("GET /categories", handleCategories)
	mux.HandleFunc("GET /buildings", handleBuildings)
	mux.HandleFunc("POST /requests/preview", handleRequestPreview)
	mux.HandleFunc("POST /requests", handleRequestCreate)
	mux.HandleFunc("GET /statuses", handleStatuses)
	mux.HandleFunc("GET /app", handleApp)
	mux.HandleFunc("GET /assets/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		http.ServeFile(w, r, "web/style.css")
	})
	mux.HandleFunc("GET /{$}", handleRoot)
	mux.HandleFunc("GET /login", handleLoginPage)

	mux.HandleFunc("POST /auth/login", handleLogin)
	mux.HandleFunc("POST /auth/logout", handleLogout)
	mux.Handle("GET /auth/me", requireDispatcher(handleAuthMe))

	mux.Handle("GET /admin", requireDispatcherPage(handleAdmin))
	mux.Handle("GET /requests", requireDispatcher(handleRequestList))
	mux.Handle("GET /requests/{number}", requireDispatcher(handleRequestByNumber))
	mux.Handle("GET /requests/{number}/history", requireDispatcher(handleRequestHistory))
	mux.Handle("PATCH /requests/{number}/status", requireDispatcher(handleRequestStatus))

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

// handleRequestList отдаёт заявки с сортировкой и фильтрами из строки запроса:
//
//	GET /requests?sort=status&order=asc&status=Новая&q=водопровод
//
// Неизвестные значения не считаются ошибкой: они заменяются значениями по
// умолчанию. Так клиент не ломается, если ему отдали ссылку из старой версии.
func handleRequestList(w http.ResponseWriter, r *http.Request) {
	filter := parseRequestFilter(r)

	requests, err := listRequests(r.Context(), filter)
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

// handleRequestHistory отдаёт журнал смены статусов.
//
// Отдельно от GET /requests/{number}, а не полем внутри заявки: список в
// /requests запрашивают все карточки сразу, и история в каждой превратилась бы в
// либо N+1 запросов, либо в огромный ответ. Здесь история грузится по кнопке и
// только для одной заявки.
func handleRequestHistory(w http.ResponseWriter, r *http.Request) {
	number := strings.TrimSpace(r.PathValue("number"))
	if number == "" {
		writeError(w, http.StatusBadRequest, "Не указан номер заявки")
		return
	}

	// У заявки, созданной до появления журнала, записей нет. Это не повод
	// отвечать 404: заявка есть, истории пока просто не нашлось.
	if _, err := findRequestByNumber(r.Context(), number); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Заявка не найдена")
			return
		}

		log.Printf("GET /requests/%s/history: %v", number, err)
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить историю")
		return
	}

	history, err := listRequestHistory(r.Context(), number)
	if err != nil {
		log.Printf("GET /requests/%s/history: %v", number, err)
		writeError(w, http.StatusInternalServerError, "Не удалось загрузить историю")
		return
	}

	writeJSON(w, http.StatusOK, history)
}

func handleRequestStatus(w http.ResponseWriter, r *http.Request) {
	number := strings.TrimSpace(r.PathValue("number"))
	if number == "" {
		writeError(w, http.StatusBadRequest, "Не указан номер заявки")
		return
	}

	var body statusChangeRequest

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommentLength+1024))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный формат запроса")
		return
	}

	body.Status = strings.TrimSpace(body.Status)
	body.Comment = strings.TrimSpace(body.Comment)

	// Всё, что можно проверить без базы, — до похода в базу. Тогда «отказ без
	// причины» стоит 400, а не 500.
	if err := validateStatusChange(body.Status, body.Comment); err != nil {
		if errors.Is(err, ErrReasonRequired) {
			writeError(w, http.StatusBadRequest, "Для отклонения заявки укажите причину")
			return
		}

		writeError(w, http.StatusBadRequest, "Неизвестный статус")
		return
	}

	if len([]rune(body.Comment)) > maxCommentLength {
		writeError(w, http.StatusBadRequest, "Комментарий слишком длинный")
		return
	}

	// Автор смены статуса — тот, кто вошёл, а не константа. Иначе в журнале у
	// всех записей стояло бы «dispatcher», и разобраться, кто что делал, было
	// бы невозможно.
	actor := loginFromContext(r.Context())
	if actor == "" {
		// Страховка на случай, если маршрут перестанут закрывать requireDispatcher.
		// Молча записать «диспетчер» значило бы потерять автора записи навсегда.
		actor = dispatcherActor
		log.Printf("PATCH /requests/%s/status: в контексте нет логина, записан %q", number, actor)
	}

	saved, err := changeRequestStatus(r.Context(), number, body.Status, actor, body.Comment)

	switch {
	case err == nil:
		log.Printf("заявка %s: статус %s (диспетчер)", number, saved.Status)
		writeJSON(w, http.StatusOK, saved)

	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "Заявка не найдена")

	case errors.Is(err, ErrSameStatus):
		writeError(w, http.StatusBadRequest, "Статус заявки не изменился")

	default:
		log.Printf("PATCH /requests/%s/status: %v", number, err)
		writeError(w, http.StatusInternalServerError, "Не удалось изменить статус")
	}
}

// handleStatuses отдаёт список статусов и список закрытых. Интерфейсу нужен
// оба: первый — чтобы построить фильтр и варианты смены, второй — чтобы
// закрытую заявку показать без выпадающего списка и не держать эту правду в
// браузере отдельно от сервера.
func handleStatuses(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"statuses": statuses,
		"closed":   closedStatusesList(),
	})
}

func handleApp(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/admin.html")
}

// handleAuthMe отдаёт логин вошедшего диспетчера.
//
// Отдельная ручка вместо заголовка на странице: браузерный JS не видит
// заголовки ответа, а логин должен быть виден в панели — иначе непонятно, чья
// это сессия. Заодно страница узнаёт, что сессия истекла, и уводит на вход, а не
// молча показывает пустой список.
func handleAuthMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"login": loginFromContext(r.Context())})
}

func handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// Уже вошедшему форма входа не нужна: сразу в панель.
	if _, err := sessionLogin(r); err == nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	http.ServeFile(w, r, "web/login.html")
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

type statusChangeRequest struct {
	Status  string `json:"status"`
	Comment string `json:"comment"`
}
