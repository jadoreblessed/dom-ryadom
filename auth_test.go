package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Стандартная стоимость 600 000 итераций в тестах слишком медленная: каждый
// тест считает хеш по несколько раз. Смысл проверок не в скорости подбора, а в
// том, что механизм работает, поэтому тут считаем дёшево. Боевая стоимость
// задана в passwordIterations и проверяется отдельным тестом на нижней границе.
const testIterations = 1_000

func testSecret() []byte {
	return []byte("тестовый-секрет-для-подписи-сессий-32")
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := formatPasswordHash("правильный пароль", []byte("соль-для-теста-1234"), testIterations)
	if err != nil {
		t.Fatalf("formatPasswordHash: %v", err)
	}

	if err := verifyPassword(hash, "правильный пароль"); err != nil {
		t.Errorf("верный пароль не подошёл: %v", err)
	}

	if err := verifyPassword(hash, "неправильный пароль"); err == nil {
		t.Error("неверный пароль подошёл — это и есть дыра")
	}
}

func TestPasswordHashIsSelfDescribing(t *testing.T) {
	// Число итераций записано внутрь хеша. Завтра стоимость поднимется, и без
	// этого пришлось бы пересчитывать хеши всех диспетчеров вручную.
	hash, err := formatPasswordHash("пароль", []byte("соль-для-теста-1234"), testIterations)
	if err != nil {
		t.Fatalf("formatPasswordHash: %v", err)
	}

	parts := strings.Split(hash, "$")
	if len(parts) != 4 {
		t.Fatalf("хеш из %d частей, ожидалось 4: %q", len(parts), hash)
	}

	if parts[0] != "pbkdf2_sha256" {
		t.Errorf("алгоритм = %q, want pbkdf2_sha256", parts[0])
	}

	if parts[1] != "1000" {
		t.Errorf("итерации = %q, want 1000 — число должно читаться из самого хеша", parts[1])
	}

	for _, part := range parts[2:] {
		if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
			t.Errorf("часть %q не читается как base64: %v", part, err)
		}
	}
}

func TestPasswordHashUsesFreshSaltEachTime(t *testing.T) {
	// Одинаковые пароли должны давать разные хеши. Иначе по хешу из утечки
	// можно было бы мгновенно сказать, у скольких сотрудников пароль «admin».
	first, err := formatPasswordHash("одинаковый", []byte("соль-для-теста-1234"), testIterations)
	if err != nil {
		t.Fatalf("formatPasswordHash: %v", err)
	}

	second, err := formatPasswordHash("одинаковый", []byte("другая-соль-12345"), testIterations)
	if err != nil {
		t.Fatalf("formatPasswordHash: %v", err)
	}

	if first == second {
		t.Error("два хеша одного пароля с разной солью совпали")
	}
}

func TestVerifyPasswordRejectsBrokenHashes(t *testing.T) {
	// Битый хеш должен давать ошибку, а не panic и не «пароль подходит».
	// Причину наружу не отдаём, но в лог она попасть обязана.
	for _, hash := range []string{
		"",
		"мусор",
		"pbkdf2_sha256$1000$только-три-части",
		"pbkdf2_sha256$не-число$соль$ключ",
		"pbkdf2_sha256$0$соль$ключ",
		"bcrypt$10$соль$ключ",
		"pbkdf2_sha256$1000$не-base64!$ключ",
	} {
		if err := verifyPassword(hash, "любой пароль"); err == nil {
			t.Errorf("verifyPassword(%q) не заметил поломку", hash)
		}
	}
}

func TestVerifyPasswordRejectsMalformedBase64(t *testing.T) {
	// Соль и ключ должны читаться: иначе битые байты молча превратились бы в
	// другой хеш, и проверка прошла бы для чужого пароля.
	hash := "pbkdf2_sha256$1000$" +
		base64.RawURLEncoding.EncodeToString([]byte("соль-для-теста-1234")) + "$" +
		"не-base64!"

	if err := verifyPassword(hash, "пароль"); err == nil {
		t.Error("нечитаемый ключ в хеше не заметен")
	}
}

func TestDummyHashRejectsEverything(t *testing.T) {
	// dummyHash подставляется, когда логина нет, чтобы время ответа не выдало
	// существующие логины. Он обязан отвергать любой пароль.
	if err := verifyPassword(dummyHash, ""); err == nil {
		t.Error("dummyHash подошёл на пустой пароль")
	}

	if err := verifyPassword(dummyHash, "любой"); err == nil {
		t.Error("dummyHash подошёл на произвольный пароль")
	}

	if !strings.HasPrefix(dummyHash, "pbkdf2_sha256$") {
		t.Errorf("dummyHash = %q, want тот же формат, что у настоящих хешей", dummyHash)
	}
}

func TestVerifyTokenRoundTrip(t *testing.T) {
	now := time.Now()

	token, err := signToken("ivanova", testSecret(), now)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	login, err := verifyToken(token, testSecret(), now)
	if err != nil {
		t.Fatalf("verifyToken: %v", err)
	}

	if login != "ivanova" {
		t.Errorf("login = %q, want ivanova", login)
	}
}

func TestVerifyTokenRejectsForgedAndTampered(t *testing.T) {
	now := time.Now()
	token, err := signToken("ivanova", testSecret(), now)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	parts := strings.Split(token, ".")

	cases := map[string]string{
		"чужой секрет": func() string {
			forged, _ := signToken("ivanova", []byte("совсем-другой-секрет-32-байта"), now)
			return forged
		}(),
		"подделанный логин":      parts[0][:len(parts[0])-1] + "X." + parts[1] + "." + parts[2],
		"подделанный срок":       parts[0] + "." + "99999999999." + parts[2],
		"подпись от другой пары": parts[0] + "." + parts[1] + ".подпись",
		"без подписи":            parts[0] + "." + parts[1] + ".",
		"лишняя точка":           token + ".лишнее",
		"пустая строка":          "",
		"не токен вовсе":         "просто текст",
		"только логин":           parts[0],
	}

	for name, value := range cases {
		if login, err := verifyToken(value, testSecret(), now); err == nil {
			t.Errorf("%s: сессия принята, вернула %q — подделка прошла", name, login)
		}
	}
}

func TestVerifyTokenRejectsExpired(t *testing.T) {
	// Сессия на 12 часов. Через 13 часов она обязана быть мёртвой, даже если
	// подпись в порядке: иначе вышедший диспетчер остаётся внутри.
	issued := time.Now()
	token, err := signToken("ivanova", testSecret(), issued)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	after := issued.Add(sessionTTL + time.Minute)
	if _, err := verifyToken(token, testSecret(), after); err == nil {
		t.Error("просроченная сессия принята")
	}
}

func TestShortSecretIsRefusedEverywhere(t *testing.T) {
	// Подпись пустым секретом принимает кто угодно: значит подделать сессию
	// можно без ключа. main() падает при старте на коротком секрете, но и
	// сами функции не должны соглашаться — иначе достаточно одного недосмотра,
	// чтобы защита исчезла молча.
	for _, secret := range [][]byte{nil, {}, []byte("короткий"), []byte(strings.Repeat("a", minSecretLength-1))} {
		token, err := signToken("ivanova", secret, time.Now())
		if err == nil {
			t.Errorf("signToken согласился на секрет длиной %d", len(secret))
		}

		if _, err := verifyToken(token, secret, time.Now()); err == nil {
			t.Errorf("verifyToken принял сессию, подписанную секретом длиной %d", len(secret))
		}
	}

	// Ровно минимальной длины — уже рабочий секрет: иначе пришлось бы писать
	// в конфиг больше, чем нужно.
	minimal := []byte(strings.Repeat("a", minSecretLength))

	token, err := signToken("ivanova", minimal, time.Now())
	if err != nil {
		t.Fatalf("секрет минимальной длины отвергнут: %v", err)
	}

	if _, err := verifyToken(token, minimal, time.Now()); err != nil {
		t.Errorf("секрет минимальной длины не работает: %v", err)
	}
}

func TestSignTokenRejectsEmptyLogin(t *testing.T) {
	// Логин не может оказаться пустым: иначе в журнале появится запись от
	// неизвестного автора, и разбор действий диспетчера станет невозможен.
	if _, err := signToken("   ", testSecret(), time.Now()); err == nil {
		t.Error("signToken принял пустой логин")
	}
}

// dispatcherRoutes — маршруты, которыми пользуется диспетчер. Каждый обязан быть
// закрыт. Список продублирован здесь намеренно: если завтра добавят новый
// маршрут диспетчера и забудут закрыть, этот тест упадёт с нехваткой строки.
var dispatcherRoutes = []struct {
	method string
	path   string
	want   int
}{
	{"GET", "/admin", http.StatusSeeOther},
	{"GET", "/requests", http.StatusUnauthorized},
	{"GET", "/requests/REQ-000001", http.StatusUnauthorized},
	{"GET", "/requests/REQ-000001/history", http.StatusUnauthorized},
	{"PATCH", "/requests/REQ-000001/status", http.StatusUnauthorized},
	{"GET", "/auth/me", http.StatusUnauthorized},
}

func TestDispatcherRoutesAreClosedWithoutSession(t *testing.T) {
	// База в этом тесте не нужна: middleware отсекает запрос раньше обработчика.
	// Именно поэтому проверка ничего не стоит по времени и не может случайно
	// пройти, упёршись в подключение.
	router := newRouter()

	for _, route := range dispatcherRoutes {
		req := httptest.NewRequest(route.method, route.path, nil)

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != route.want {
			t.Errorf("%s %s без сессии -> %d, want %d: маршрут открыт",
				route.method, route.path, rec.Code, route.want)
		}
	}
}

func TestResidentRoutesStayOpen(t *testing.T) {
	// Подача заявки жителем не должна требовать входа: иначе некому будет
	// оставлять заявки.
	//
	// База в тестах не поднята, поэтому проверяем только те маршруты, которые
	// до обращения к базе не доходят: справочные страницы и разбор тела запроса.
	// Ответ 400 на пустом теле означает «запрос дошёл до обработчика и был
	// отклонён разбором» — до базы дело не дошло, и стена авторизации точно не
	// сработала.
	router := newRouter()

	t.Run("страницы и справочники", func(t *testing.T) {
		pages := []struct {
			path string
			want int
		}{
			{"/app", http.StatusOK},
			{"/login", http.StatusOK},
			{"/statuses", http.StatusOK},
		}

		for _, page := range pages {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("GET", page.path, nil))

			if rec.Code != page.want {
				t.Errorf("GET %s -> %d, want %d", page.path, rec.Code, page.want)
			}
		}
	})

	t.Run("подача заявки доходит до разбора", func(t *testing.T) {
		for _, path := range []string{"/requests", "/requests/preview"} {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader("")))

			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s -> %d, want 400: запрос не дошёл до разбора тела, "+
					"значит его остановила авторизация", path, rec.Code)
			}
		}
	})
}

func TestGarbageSessionCookieIsRejected(t *testing.T) {
	// Подставленная вручную или испорченная cookie должна отсекаться так же,
	// как её отсутствие.
	router := newRouter()

	req := httptest.NewRequest("GET", "/requests", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "forged.123.456"})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("испорченная cookie дала %d, want 401", rec.Code)
	}
}

func TestValidSessionReachesHandlerWithLogin(t *testing.T) {
	// Проверяем не просто «сессия прошла», а что логин дошёл до контекста.
	// /auth/me базу не трогает, поэтому тест ничего не стоит по времени.
	// Именно из контекста берётся автор записи в журнале смены статусов, и
	// потеря логина здесь означала бы, что в журнале снова «dispatcher».
	t.Setenv("AUTH_SECRET", string(testSecret()))

	now := time.Now()
	token, err := signToken("ivanova", testSecret(), now)
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})

	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("валидная сессия отсечена: %d (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Login string `json:"login"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}

	if body.Login != "ivanova" {
		t.Errorf("login = %q, want ivanova", body.Login)
	}
}

func TestSessionSignedWithOtherSecretIsRejected(t *testing.T) {
	// Секрет задаётся снаружи, и подпись из-под другого секрета — это чужой
	// сервер или подделка. Такая сессия обязана отсекаться.
	t.Setenv("AUTH_SECRET", string(testSecret()))

	other := []byte("другой-секрет-в-этом-же-приложении")

	token, err := signToken("ivanova", other, time.Now())
	if err != nil {
		t.Fatalf("signToken: %v", err)
	}

	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})

	rec := httptest.NewRecorder()
	newRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("сессия под чужим секретом принята: %d", rec.Code)
	}
}

func TestGeneratePasswordIsRandomAndStrong(t *testing.T) {
	// Пароль, который печатается в лог при первом запуске, обязан быть
	// непредсказуемым: он единственный раз попадает в открытый текст.
	first, err := generatePassword()
	if err != nil {
		t.Fatalf("generatePassword: %v", err)
	}

	second, err := generatePassword()
	if err != nil {
		t.Fatalf("generatePassword: %v", err)
	}

	if first == second {
		t.Error("два пароля совпали — генератор не случайный")
	}

	// 12 байт энтропии дают 16 символов base64. Меньше 12 байт пароль
	// перебирается, больше — превращается в нечитаемый ключ в логах.
	if len(first) != 16 {
		t.Errorf("длина пароля %d символов (строка %q), ждали 16", len(first), first)
	}

	if err := verifyPassword(mustHash(t, first), first); err != nil {
		t.Errorf("сгенерированный пароль не проходит проверку: %v", err)
	}
}

func mustHash(t *testing.T, password string) string {
	t.Helper()

	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}

	return hash
}
