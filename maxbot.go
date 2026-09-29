package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	Text string `json:"text"`
}

type maxMessage struct {
	Sender maxUser         `json:"sender"`
	Body   *maxMessageBody `json:"body"`
}

type maxUpdate struct {
	UpdateType string      `json:"update_type"`
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
	Stage     maxDialogStage
	Category  string
	Building  Building
	Buildings []Building
}

type maxBot struct {
	token  string
	apiURL string
	client *http.Client

	mu      sync.Mutex
	dialogs map[int64]maxDialogState
	list    func(context.Context) ([]Building, error)
	find    func(context.Context, string) (Building, error)
	create  func(context.Context, Building, Request) (SavedRequest, error)
}

func newMaxBot(token string) *maxBot {
	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MAX_API_URL")), "/")
	if apiURL == "" {
		apiURL = defaultMaxAPIURL
	}

	return &maxBot{
		token:   strings.TrimSpace(token),
		apiURL:  apiURL,
		client:  &http.Client{Timeout: (maxPollTimeout + 10) * time.Second},
		dialogs: make(map[int64]maxDialogState),
		list:    getAllBuildings,
		find:    findBuilding,
		create:  createRequest,
	}
}

// run получает события MAX через Long Polling. Для локальной разработки это
// позволяет принимать сообщения без публичного HTTPS-адреса. На production
// этот цикл заменяется webhook-подпиской, сама обработка диалога остаётся той же.
func (b *maxBot) run(ctx context.Context) {
	log.Println("MAX-бот запущен в режиме Long Polling")

	var marker *int64
	retryDelay := time.Second

	for ctx.Err() == nil {
		updates, nextMarker, err := b.getUpdates(ctx, marker)
		if err != nil {
			log.Printf("MAX: получение событий: %v", err)
			if !waitContext(ctx, retryDelay) {
				return
			}
			if retryDelay < 30*time.Second {
				retryDelay *= 2
			}
			continue
		}

		retryDelay = time.Second
		if nextMarker != nil {
			marker = nextMarker
		}

		for _, update := range updates {
			if err := b.handleUpdate(ctx, update); err != nil {
				log.Printf("MAX: обработка события %q: %v", update.UpdateType, err)
			}
		}
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
		return b.handleText(ctx, update.Message.Sender.UserID, update.Message.Body.Text)
	}

	return nil
}

func (b *maxBot) handleText(ctx context.Context, userID int64, text string) error {
	text = strings.TrimSpace(text)
	normalized := strings.ToLower(text)

	switch normalized {
	case "/start", "/new", "новая заявка", "создать заявку":
		return b.startDialog(ctx, userID)
	case "/cancel", "отмена":
		b.deleteDialog(userID)
		return b.sendMessage(ctx, userID, "Создание заявки отменено. Чтобы начать заново, напишите /new")
	}

	state, ok := b.dialog(userID)
	if !ok {
		return b.startDialog(ctx, userID)
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
		b.setDialog(userID, state)

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
		b.setDialog(userID, state)

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

		saved, err := b.create(ctx, state.Building, req)
		if err != nil {
			return fmt.Errorf("сохранение заявки: %w", err)
		}

		b.deleteDialog(userID)
		answer := fmt.Sprintf(
			"✅ Заявка создана\n\nНомер: %s\nАдрес: %s\nКатегория: %s\nСтатус: %s\nОтветственный: %s\n\nЧто делать дальше:\n%s\n\nЧтобы создать ещё одну заявку, напишите /new",
			saved.Number,
			saved.Request.Address,
			saved.Request.Category,
			saved.Status,
			saved.Responsible,
			saved.NextStep,
		)
		return b.sendMessage(ctx, userID, answer)
	}

	b.deleteDialog(userID)
	return b.startDialog(ctx, userID)
}

func (b *maxBot) startDialog(ctx context.Context, userID int64) error {
	b.setDialog(userID, maxDialogState{Stage: maxStageCategory})
	return b.sendMessage(ctx, userID,
		"Здравствуйте! Я помогу создать заявку по вашему дому.\n\nВыберите категорию — отправьте цифру:\n\n"+
			categoryList()+"\n\nДля отмены напишите /cancel")
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

func (b *maxBot) dialog(userID int64) (maxDialogState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.dialogs[userID]
	return state, ok
}

func (b *maxBot) setDialog(userID int64, state maxDialogState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dialogs[userID] = state
}

func (b *maxBot) deleteDialog(userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.dialogs, userID)
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
