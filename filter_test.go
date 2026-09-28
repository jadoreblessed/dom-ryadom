package main

import (
	"net/http/httptest"
	"testing"
)

func TestNormalizeRejectsUnknownSort(t *testing.T) {
	// Ключ сортировки подставляется в SQL. Всё, чего нет в белом списке,
	// обязано превратиться в значение по умолчанию.
	cases := []string{
		"created_at; DROP TABLE requests",
		"r.number",
		"number DESC",
		"",
		"нет такого",
	}

	for _, value := range cases {
		f := RequestFilter{Sort: value}
		f.Normalize()

		if f.Sort != defaultSort {
			t.Errorf("sort=%q обработан как %q, ожидался %q", value, f.Sort, defaultSort)
		}
	}
}

func TestNormalizeRejectsUnknownOrder(t *testing.T) {
	// Направление сортировки уходит в SQL как есть, поэтому допустимы
	// ровно два значения.
	cases := map[string]string{
		"asc":                "asc",
		"desc":               "desc",
		"ASC":                "desc",
		"asc, id":            "desc",
		"desc; DROP TABLE x": "desc",
		"":                   "desc",
		"asc nulls first":    "desc",
		"asc, (select 1)":    "desc",
	}

	for value, want := range cases {
		f := RequestFilter{Order: value}
		f.Normalize()

		if f.Order != want {
			t.Errorf("order=%q дал %q, ожидалось %q", value, f.Order, want)
		}

		sql := f.orderSQL()
		if sql != "ASC" && sql != "DESC" {
			t.Errorf("orderSQL() вернул %q — это не ASC и не DESC", sql)
		}
	}

	if (RequestFilter{Order: "asc"}).orderSQL() != "ASC" {
		t.Error("order=asc должен давать ASC")
	}

	if (RequestFilter{Order: "desc"}).orderSQL() != "DESC" {
		t.Error("order=desc должен давать DESC")
	}
}

func TestSortSQLAlwaysComesFromWhitelist(t *testing.T) {
	for key, column := range requestSortColumns {
		f := RequestFilter{Sort: key}
		f.Normalize()

		if got := f.sortSQL(); got != column {
			t.Errorf("sort=%q дал колонку %q, ожидалась %q", key, got, column)
		}
	}
}

func TestNormalizeDropsUnknownStatus(t *testing.T) {
	// Неизвестный статус не должен попадать в WHERE: там он просто ничего
	// не найдёт, и список молча окажется пустым. Лучше проигнорировать.
	f := RequestFilter{Status: "Выдуманный"}
	f.Normalize()

	if f.Status != "" {
		t.Errorf("статус %q не отброшен, остался %q", "Выдуманный", f.Status)
	}

	f = RequestFilter{Status: "  В работе  "}
	f.Normalize()

	if f.Status != statusInWork {
		t.Errorf("статус с пробелами не обрезан: %q", f.Status)
	}
}

func TestNormalizeClampsLimit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{0, defaultLimit},
		{-5, defaultLimit},
		{10, 10},
		{maxLimit, maxLimit},
		{maxLimit + 1000, maxLimit},
	}

	for _, c := range cases {
		f := RequestFilter{Limit: c.limit}
		f.Normalize()

		if f.Limit != c.want {
			t.Errorf("limit=%d стал %d, ожидалось %d", c.limit, f.Limit, c.want)
		}
	}
}

func TestNormalizeTrimsAndCapsSearch(t *testing.T) {
	f := RequestFilter{Search: "   труба   "}
	f.Normalize()

	if f.Search != "труба" {
		t.Errorf("поиск не обрезан по пробелам: %q", f.Search)
	}

	long := make([]rune, maxSearchLength+50)
	for i := range long {
		long[i] = 'я'
	}

	f = RequestFilter{Search: string(long)}
	f.Normalize()

	if got := len([]rune(f.Search)); got != maxSearchLength {
		t.Errorf("длинный поиск не обрезан: %d символов вместо %d", got, maxSearchLength)
	}
}

func TestEscapeLikeNeutralizesWildcards(t *testing.T) {
	// Без экранирования пользовательский «%» превратит «ищу трубу» в
	// «ищу вообще всё».
	cases := map[string]string{
		`труба`:   `труба`,
		`100%`:    `100\%`,
		`a_b`:     `a\_b`,
		`a%b_c`:   `a\%b\_c`,
		`back\sl`: `back\\sl`,
	}

	for input, want := range cases {
		if got := escapeLike(input); got != want {
			t.Errorf("escapeLike(%q) = %q, ожидалось %q", input, got, want)
		}
	}
}

func TestParseRequestFilterReadsQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/requests?sort=address&order=asc&status=Новая&q=Ленина&limit=5", nil)

	f := parseRequestFilter(r)

	if f.Sort != "address" {
		t.Errorf("sort=%q", f.Sort)
	}

	if f.Order != "asc" {
		t.Errorf("order=%q", f.Order)
	}

	if f.Status != statusNew {
		t.Errorf("status=%q", f.Status)
	}

	if f.Search != "Ленина" {
		t.Errorf("search=%q", f.Search)
	}

	if f.Limit != 5 {
		t.Errorf("limit=%d", f.Limit)
	}
}

func TestParseRequestFilterSurvivesGarbage(t *testing.T) {
	// Ссылка могла прийти из старой версии интерфейса или из рукописи
	// пользователя. Сервер не должен на этом падать.
	urls := []string{
		"/requests",
		"/requests?sort=&order=&status=&q=&limit=",
		"/requests?sort=1;DROP&order=x&limit=abc",
		"/requests?limit=-1",
	}

	for _, url := range urls {
		r := httptest.NewRequest("GET", url, nil)

		f := parseRequestFilter(r)

		if f.Sort != defaultSort {
			t.Errorf("%s: sort=%q, ожидался %q", url, f.Sort, defaultSort)
		}

		if f.Order != defaultOrder {
			t.Errorf("%s: order=%q, ожидался %q", url, f.Order, defaultOrder)
		}

		if f.Limit != defaultLimit {
			t.Errorf("%s: limit=%d, ожидалось %d", url, f.Limit, defaultLimit)
		}
	}
}

func TestSortLabelsCoverEverySortableColumn(t *testing.T) {
	// Иначе в выпадающем списке появится колонка без подписи, а подпись
	// окажется без работающей колонки.
	labelled := map[string]bool{}

	for _, item := range requestSortLabels {
		if _, ok := requestSortColumns[item.Value]; !ok {
			t.Errorf("в списке подписей есть %q, которой нет в списке колонок", item.Value)
		}

		labelled[item.Value] = true
	}

	for key := range requestSortColumns {
		if !labelled[key] {
			t.Errorf("колонка %q есть, но подписи для неё нет", key)
		}
	}
}
