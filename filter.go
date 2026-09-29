package main

import (
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultSort     = "created_at"
	defaultOrder    = "desc"
	defaultLimit    = 200
	maxLimit        = 500
	maxSearchLength = 100
)

// requestSortColumns — белый список колонок для сортировки.
//
// Ключ приходит от клиента, значение подставляется в SQL. Подставлять имя
// колонки из пользовательского ввода нельзя: это SQL-инъекция. Поэтому клиент
// может выбрать только из этого списка, а всё остальное молча заменяется
// значением по умолчанию.
var requestSortColumns = map[string]string{
	"created_at":   "r.created_at",
	"updated_at":   "r.updated_at",
	"number":       "r.number",
	"status":       "r.status",
	"address":      "b.address",
	"category":     "r.category",
	"managing_org": "m.name",
	"responsible":  "r.responsible",
}

// requestSortLabels — подписи для интерфейса. Держим рядом со списком колонок,
// чтобы не расходились при добавлении варианта.
var requestSortLabels = []struct {
	Value string
	Title string
}{
	{"created_at", "Сначала новые"},
	{"updated_at", "По последнему изменению"},
	{"number", "По номеру"},
	{"status", "По статусу"},
	{"address", "По адресу"},
	{"category", "По категории"},
	{"managing_org", "По управляющей организации"},
	{"responsible", "По ответственному"},
}

type RequestFilter struct {
	Sort   string
	Order  string
	Status string
	Search string
	Limit  int
}

// orderSQL возвращает направление сортировки. Только «ASC» или «DESC» —
// строка не может прийти извне, иначе это снова инъекция.
func (f RequestFilter) orderSQL() string {
	if f.Order == "asc" {
		return "ASC"
	}

	return "DESC"
}

func (f RequestFilter) sortSQL() string {
	// Нормализация гарантирует, что ключ есть в списке. Двойная проверка
	// дешёвая и защищает, если кто-то добавит поле в структуру, забыв про
	// Normalize.
	column, ok := requestSortColumns[f.Sort]
	if !ok {
		column = requestSortColumns[defaultSort]
	}

	return column
}

func (f *RequestFilter) Normalize() {
	if _, ok := requestSortColumns[f.Sort]; !ok {
		f.Sort = defaultSort
	}

	if f.Order != "asc" && f.Order != "desc" {
		f.Order = defaultOrder
	}

	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}

	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}

	f.Status = strings.TrimSpace(f.Status)
	f.Search = strings.TrimSpace(f.Search)

	if f.Status != "" && !isKnownStatus(f.Status) {
		f.Status = ""
	}

	if len([]rune(f.Search)) > maxSearchLength {
		f.Search = string([]rune(f.Search)[:maxSearchLength])
	}
}

// escapeLike экранирует спецсимволы LIKE, иначе пользовательский ввод «%»
// превратит «ищу трубу» в «ищу всё подряд».
func escapeLike(value string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
	).Replace(value)
}

func parseRequestFilter(r *http.Request) RequestFilter {
	query := r.URL.Query()

	limit := defaultLimit
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	filter := RequestFilter{
		Sort:   query.Get("sort"),
		Order:  query.Get("order"),
		Status: query.Get("status"),
		Search: query.Get("q"),
		Limit:  limit,
	}

	filter.Normalize()

	return filter
}
