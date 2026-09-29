package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultMaxAPIURL = "https://platform-api2.max.ru"
	maxPollTimeout   = 30
)

var botCategories = []string{
	"Протечка",
	"Нет отопления",
	"Не работает лифт",
	"Нет освещения",
	"Другое",
}

type maxUser struct {
	UserID int64 `json:"user_id"`
	IsBot  bool  `json:"is_bot"`
}

type maxMessageBody struct {
	Mid  string `json:"mid"`
	Text string `json:"text"`
}

type maxMessage struct {
	Sender maxUser         `json:"sender"`
	Body   *maxMessageBody `json:"body"`
}

type maxUpdate struct {
	UpdateType string      `json:"update_type"`
	Timestamp  int64       `json:"timestamp"`
	User       *maxUser    `json:"user"`
	Message    *maxMessage `json:"message"`
}

type maxUpdatesResponse struct {
	Updates []maxUpdate `json:"updates"`
	Marker  *int64      `json:"marker"`
}

type maxDialogStage int

const (
	maxStageCategory maxDialogStage = iota + 1
	maxStageBuilding
	maxStageDescription
)

type maxDialogState struct {
	Stage         maxDialogStage
	Category      string
	Building      Building
	Buildings     []Building
	LastMessageID string
}

type maxBot struct {
	token     string
	apiURL    string
	client    *http.Client

	mu        sync.Mutex
	dialogs   map[int64]maxDialogState
	list      func(context.Context) ([]Building, error)
	find      func(context.Context, string) (Building, error)
	create    func(context.Context, Building, Request, int64, string) (SavedRequest, error)
	mine      func(context.Context, int64) ([]SavedRequest, error)
	lookup    func(context.Context, int64, string) (SavedRequest, error)
	byMessage func(context.Context, int64, string) (SavedRequest, error)
	save      func(context.Context, int64, maxDialogState) error
	remove    func(context.Context, int64) error
	load      func(context.Context, int64) (maxDialogState, bool, error)
}

func newMaxBot(token string) *maxBot {
	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MAX_API_URL")), "/")
	if apiURL == "" {
		apiURL = defaultMaxAPIURL
	}

	return &maxBot{
		token:     strings.TrimSpace(token),
		apiURL:    apiURL,
		client:    &http.Client{Timeout: (maxPollTimeout + 10) * time.Second},
		dialogs:   make(map[int64]maxDialogState),
		list:      getAllBuildings,
		find:      findBuilding,
		create:    createRequestForMax,
		mine:      listRequestsForMax,
		lookup:    findRequestForMax,
		byMessage: findRequestByMaxMessage,
		save:      saveMaxDialog,
		remove:    removeMaxDialog,
		load:      readMaxDialog,
	}
}

// run получает события MAX через Long Polling. Для локальной разработки это
// позволяет принимать сообщения без публичного HTTPS-адреса. На production
// этот цикл заменяется webhook-подпиской, сама обработка диалога остаётся той же.
func (b *maxBot) run(ctx context.Context) {
	log.Println("MAX-бот запущен в режиме Long Polling")

	retryDelay := time.Second

	for ctx.Err() == nil {
		marker, err := readMaxMarker(ctx)
		if err != nil {
			log.Printf("MAX: чтение курсора: %v", err)
			if !waitContext(ctx, retryDelay) {
				return
			}
			if retryDelay < 30*time.Second {
				retryDelay *= 2
			}
			continue
		}
		updates, nextMarker, err := b.getUpdates(ctx, marker)
		if err == nil {
			err = storeMaxUpdates(ctx, updates, nextMarker)
		}
		if err != nil {
			log.Printf("MAX: получение/сохранение событий: %v", err)
			if !waitContext(ctx, retryDelay) {
				return
			}
			if retryDelay < 30*time.Second {
				retryDelay *= 2
			}
			continue
		}

		retryDelay = time.Second
	}
}

func (b *maxBot) getUpdates(ctx context.Context, marker *int64) ([]maxUpdate, *int64, error) {
	endpoint, err := url.Parse(b.apiURL + "/updates")
	if err != nil {
		return nil, marker, err
	}

	query := endpoint.Query()
	query.Set("limit", "100")
	query.Set("timeout", strconv.Itoa(maxPollTimeout))
	query.Set("types", "message_created,bot_started")
	if marker != nil {
		query.Set("marker", strconv.FormatInt(*marker, 10))
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, marker, err
	}
	req.Header.Set("Authorization", b.token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, marker, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, marker, fmt.Errorf("API вернул %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var result maxUpdatesResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return nil, marker, fmt.Errorf("не удалось разобрать ответ: %w", err)
	}

	return result.Updates, result.Marker, nil
}

func (b *maxBot) handleUpdate(ctx context.Context, update maxUpdate) error {
	switch update.UpdateType {
	case "bot_started":
		if update.User == nil || update.User.UserID == 0 || update.User.IsBot {
			return nil
		}
		return b.startDialog(ctx, update.User.UserID)

	case "message_created":
		if update.Message == nil || update.Message.Body == nil ||
			update.Message.Sender.UserID == 0 || update.Message.Sender.IsBot {
			return nil
		}
		return b.handleTextWithMessage(ctx, update.Message.Sender.UserID, update.Message.Body.Text, update.Message.Body.Mid)
	}

	return nil
}

func (b *maxBot) handleText(ctx context.Context, userID int64, text string) error {
	return b.handleTextWithMessage(ctx, userID, text, "")
}

func (b *maxBot) handleTextWithMessage(ctx context.Context, userID int64, text, messageID string) error {
	if messageID != "" && b.byMessage != nil {
		saved, err := b.byMessage(ctx, userID, messageID)
		if err == nil {
			return b.sendMessage(ctx, userID, createdRequestText(saved))
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("поиск сообщения MAX: %w", err)
		}
	}
	text = strings.TrimSpace(text)
	normalized := strings.ToLower(text)

	switch normalized {
	case "/help", "помощь":
		return b.sendMessage(ctx, userID, "Команды:\n/new — создать заявку\n/my — мои заявки\n/status REQ-000001 — статус по номеру\n/cancel — отменить ввод")
	case "/start", "/new", "новая заявка", "создать заявку":
		return b.startDialog(ctx, userID)
	case "/my", "мои заявки":
		requests, err := b.mine(ctx, userID)
		if err != nil {
			return fmt.Errorf("список заявок MAX: %w", err)
		}
		if len(requests) == 0 {
			return b.sendMessage(ctx, userID, "У вас пока нет заявок. Напишите /new, чтобы создать первую.")
		}
		var lines []string
		for _, saved := range requests {
			lines = append(lines, fmt.Sprintf("%s · %s · %s", saved.Number, saved.Status, saved.Request.Category))
		}
		return b.sendMessage(ctx, userID, "Ваши последние заявки:\n"+strings.Join(lines, "\n")+"\n\nНапишите /status REQ-000001 для подробностей.")
	case "/cancel", "отмена":
		if err := b.deleteDialog(ctx, userID); err != nil {
			return err
		}
		return b.sendMessage(ctx, userID, "Создание заявки отменено. Чтобы начать заново, напишите /new")
	case "/status":
		return b.sendMessage(ctx, userID, "Укажите номер: /status REQ-000001. Список своих заявок — /my.")
	}
	if strings.HasPrefix(normalized, "/status ") {
		number := strings.ToUpper(strings.TrimSpace(text[len("/status "):]))
		saved, err := b.lookup(ctx, userID, number)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return b.sendMessage(ctx, userID, "Заявка не найдена среди ваших обращений. Проверьте номер или напишите /my.")
			}
			return fmt.Errorf("статус заявки MAX: %w", err)
		}
		answer := fmt.Sprintf("%s · %s\n%s\n%s", saved.Number, saved.Status, saved.Request.Address, saved.Request.Category)
		if saved.Comment != "" {
			answer += "\nКомментарий: " + saved.Comment
		}
		return b.sendMessage(ctx, userID, answer)
	}

	state, ok := b.dialog(userID)
	if !ok && b.load != nil {
		var err error
		state, ok, err = b.load(ctx, userID)
		if err != nil {
			return fmt.Errorf("восстановление диалога: %w", err)
		}
	}
	if !ok {
		return b.startDialog(ctx, userID)
	}
	if messageID != "" && state.LastMessageID == messageID {
		switch state.Stage {
		case maxStageBuilding:
			return b.sendMessage(ctx, userID, "Выберите дом — отправьте его номер:\n\n"+buildingList(state.Buildings))
		case maxStageDescription:
			return b.sendMessage(ctx, userID, "Опишите проблему одним сообщением. Например: течёт труба на первом этаже.")
		}
	}

	switch state.Stage {
	case maxStageCategory:
		category, ok := chooseCategory(text)
		if !ok {
			return b.sendMessage(ctx, userID, "Не понял категорию. Отправьте цифру от 1 до 5:\n\n"+categoryList())
		}

		buildings, err := b.list(ctx)
		if err != nil {
			return fmt.Errorf("загрузка домов: %w", err)
		}
		if len(buildings) == 0 {
			return b.sendMessage(ctx, userID, "Пока нет доступных домов. Попробуйте позже.")
		}

		state.Stage = maxStageBuilding
		state.Category = category
		state.Buildings = buildings
		state.LastMessageID = messageID
		if err := b.setDialog(ctx, userID, state); err != nil {
			return err
		}

		return b.sendMessage(ctx, userID, "Выберите дом — отправьте его номер:\n\n"+buildingList(buildings))

	case maxStageBuilding:
		building, ok := chooseBuilding(text, state.Buildings)
		if !ok {
			return b.sendMessage(ctx, userID, "Не нашёл такой дом. Отправьте номер из списка:\n\n"+buildingList(state.Buildings))
		}

		// Повторно проверяем дом в базе: между показом списка и выбором запись
		// могла измениться или быть удалена.
		building, err := b.find(ctx, building.ID)
		if err != nil {
			return fmt.Errorf("поиск дома %s: %w", building.ID, err)
		}

		state.Stage = maxStageDescription
		state.Building = building
		state.Buildings = nil
		state.LastMessageID = messageID
		if err := b.setDialog(ctx, userID, state); err != nil {
			return err
		}

		return b.sendMessage(ctx, userID, "Опишите проблему одним сообщением. Например: течёт труба на первом этаже.")

	case maxStageDescription:
		if text == "" {
			return b.sendMessage(ctx, userID, "Описание не должно быть пустым. Напишите, что произошло.")
		}
		if len([]rune(text)) > maxDescription {
			return b.sendMessage(ctx, userID, fmt.Sprintf("Описание слишком длинное. Максимум — %d символов.", maxDescription))
		}

		req := Request{
			Category:    state.Category,
			Address:     state.Building.Address,
			Description: text,
			BuildingID:  state.Building.ID,
		}

		saved, err := b.create(ctx, state.Building, req, userID, messageID)
		if err != nil {
			return fmt.Errorf("сохранение заявки: %w", err)
		}

		if err := b.deleteDialog(ctx, userID); err != nil {
			log.Printf("MAX: заявка %s сохранена, не удалось очистить диалог: %v", saved.Number, err)
		}
		return b.sendMessage(ctx, userID, createdRequestText(saved))
	}

	if err := b.deleteDialog(ctx, userID); err != nil {
		return err
	}
	return b.startDialog(ctx, userID)
}

func createdRequestText(saved SavedRequest) string {
	return fmt.Sprintf(
		"✅ Заявка создана\n\nНомер: %s\nАдрес: %s\nКатегория: %s\nСтатус: %s\nОтветственный: %s\n\nЧто делать дальше:\n%s\n\nЧтобы создать ещё одну заявку, напишите /new",
		saved.Number,
		saved.Request.Address,
		saved.Request.Category,
		saved.Status,
		saved.Responsible,
		saved.NextStep,
	)
}

func (b *maxBot) startDialog(ctx context.Context, userID int64) error {
	if err := b.setDialog(ctx, userID, maxDialogState{Stage: maxStageCategory}); err != nil {
		return err
	}
	return b.sendMessage(ctx, userID,
		"Здравствуйте! Я помогу создать заявку по вашему дому.\n\nВыберите категорию — отправьте цифру:\n\n"+
			categoryList()+"\n\nМои заявки: /my · Отмена: /cancel")
}

func (b *maxBot) sendMessage(ctx context.Context, userID int64, text string) error {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/messages?user_id=%d", b.apiURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", b.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("отправка сообщения: API вернул %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return nil
}

func (b *maxBot) notifyStatusChange(ctx context.Context, saved SavedRequest) error {
	if saved.MaxUserID == nil {
		return nil
	}
	return b.sendMessage(ctx, *saved.MaxUserID, statusNotificationText(saved))
}

func statusNotificationText(saved SavedRequest) string {
	message := fmt.Sprintf("Статус заявки %s изменён: %s", saved.Number, saved.Status)
	if saved.Comment != "" {
		message += "\nКомментарий: " + saved.Comment
	}
	return message
}

func (b *maxBot) dialog(userID int64) (maxDialogState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.dialogs[userID]
	return state, ok
}

func (b *maxBot) setDialog(ctx context.Context, userID int64, state maxDialogState) error {
	if b.save != nil {
		if err := b.save(ctx, userID, state); err != nil {
			return fmt.Errorf("сохранение диалога MAX: %w", err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dialogs[userID] = state
	return nil
}

func (b *maxBot) deleteDialog(ctx context.Context, userID int64) error {
	if b.remove != nil {
		if err := b.remove(ctx, userID); err != nil {
			return fmt.Errorf("очистка диалога MAX: %w", err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.dialogs, userID)
	return nil
}

func categoryList() string {
	var lines []string
	for i, category := range botCategories {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, category))
	}
	return strings.Join(lines, "\n")
}

func chooseCategory(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if number, err := strconv.Atoi(input); err == nil && number >= 1 && number <= len(botCategories) {
		return botCategories[number-1], true
	}

	for _, category := range botCategories {
		if strings.EqualFold(input, category) {
			return category, true
		}
	}
	return "", false
}

func buildingList(buildings []Building) string {
	var lines []string
	for i, building := range buildings {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, building.Address))
	}
	return strings.Join(lines, "\n")
}

func chooseBuilding(input string, buildings []Building) (Building, bool) {
	input = strings.TrimSpace(input)
	if number, err := strconv.Atoi(input); err == nil && number >= 1 && number <= len(buildings) {
		return buildings[number-1], true
	}

	for _, building := range buildings {
		if strings.EqualFold(input, building.ID) || strings.EqualFold(input, building.Address) {
			return building, true
		}
	}
	return Building{}, false
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
