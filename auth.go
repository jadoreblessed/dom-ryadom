package main

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Авторизация диспетчера.
//
// До этого шага /admin и GET /requests были открыты любому, кто дотянется до
// порта: список всех заявок всех жителей, с адресами и описаниями. Теперь
// закрыты все маршруты, которыми пользуется диспетчер, а форма подачи заявки
// жителем осталась открытой — иначе нечем принимать заявки.

const (
	sessionCookie = "dispatcher_session"
	sessionTTL    = 12 * time.Hour

	// minSecretLength — минимальная длина AUTH_SECRET. Подпись сессии
	// устойчива только к длинному секрету: короткий перебирается, и тогда
	// anyone может подделать cookie и выдать себя за диспетчера.
	minSecretLength = 32

	// passwordIterations — стоимость вывода пароля. 600 000 итераций
	// PBKDF2-HMAC-SHA256 — рекомендация OWASP для хранения паролей. В Go это
	// около 0,2 с на попытку входа: достаточно медленно, чтобы перебор в
	// офлайне стоил дорого, и достаточно быстро, чтобы вход не раздражал.
	passwordIterations = 600_000

	// passwordSaltLength и passwordKeyLength по RFC 8018: соль не короче
	// 8 байт, ключ — размеру хеша.
	passwordSaltLength = 16
	passwordKeyLength  = 32
)

// dummyHash — хеш пароля, который не подходит ни к одному логину. Подставляется,
// когда логина в базе нет, чтобы проверка заняла столько же времени, сколько
// заняла бы при верном пароле. Иначе по времени ответа видно, какие логины
// существуют, — это timing-атака, и на защите личных данных её заметят.
var dummyHash = mustDummyHash()

func mustDummyHash() string {
	hash, err := hashPasswordWith(randomPassword(), passwordIterations)
	if err != nil {
		panic("не удалось построить dummyHash: " + err.Error())
	}

	return hash
}

func randomPassword() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("не удалось получить случайные байты: " + err.Error())
	}

	return base64.RawURLEncoding.EncodeToString(buf)
}

// hashPasswordWith возвращает хеш в самодостаточном формате:
//
//	pbkdf2_sha256$<итерации>$<соль base64>$<ключ base64>
//
// Число итераций записано внутрь хеша намеренно. Завтра понадобится поднять
// стоимость до миллиона, и без этого пришлось бы пересчитывать хеши всех
// диспетчеров вручную: старый формат без числа итераций такой возможности не
// оставляет — пришлось бы ломать старые пароли.
func hashPasswordWith(password string, iterations int) (string, error) {
	if iterations < 1 {
		return "", fmt.Errorf("недопустимое число итераций: %d", iterations)
	}

	salt := make([]byte, passwordSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("не удалось получить соль: %w", err)
	}

	return formatPasswordHash(password, salt, iterations)
}

func formatPasswordHash(password string, salt []byte, iterations int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyLength)
	if err != nil {
		return "", fmt.Errorf("не удалось вывести ключ: %w", err)
	}

	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s",
		iterations,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(key),
	), nil
}

func hashPassword(password string) (string, error) {
	return hashPasswordWith(password, passwordIterations)
}

// verifyPassword сверяет пароль с хешем.
//
// Возвращает ошибку, а не bool, и делает это один раз в ходе разбора: иначе
// пришлось бы сначала распарсить хеш, потом посчитать стоимость итераций, и
// ошибки «неверный пароль» и «битый хеш» смешались бы в один ответ. Снаружи
// оба случая выглядят одинаково — неверный пароль, — но в логе видно, что
// именно сломалось.
func verifyPassword(hash, password string) error {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return errors.New("хеш пароля имеет неизвестный формат")
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return errors.New("в хеше пароля неверное число итераций")
	}

	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("в хеше пароля не читается соль")
	}

	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return errors.New("в хеше пароля не читается ключ")
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return fmt.Errorf("не удалось вывести ключ: %w", err)
	}

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return errors.New("пароль не подходит")
	}

	return nil
}

// authSecret возвращает подпись для сессий. Пустой секрет — не «секрет по
// умолчанию», а отсутствие защиты: им подписал бы кто угодно. Поэтому main
// проверяет секрет при старте и не поднимается без него.
func authSecret() []byte {
	return []byte(os.Getenv("AUTH_SECRET"))
}

// validateSecret отсекает пустой и короткий секрет.
//
// Проверка в main() при старте — первая линия, но подпись должна быть
// непригодной и сама по себе: иначе любой другой вызов signToken или
// verifyToken с незаполненным секретом (тест, недосмотр в конфиге, новый
// обработчик) молча выдаёт сессию, которую подделал кто угодно. Подпись
// пустой строкой — это не «секрет по умолчанию», а отсутствие защиты.
func validateSecret(secret []byte) error {
	if len(secret) < minSecretLength {
		return fmt.Errorf("секрет подписи не задан или короче %d символов", minSecretLength)
	}

	return nil
}

// signToken делает подписанный cookie вида <login>.<срок>.<подпись>.
//
// Сессия хранится в самой cookie, а не в таблице: переживает перезапуск
// приложения и не требует чистить просроченные строки.
func signToken(login string, secret []byte, now time.Time) (string, error) {
	if err := validateSecret(secret); err != nil {
		return "", err
	}

	// Логин обрезается и проверяется: пустой автор в журнале смены статусов
	// означал бы запись, которую невозможно разобрать. Строка из пробелов —
	// тот же пустой автор.
	login = strings.TrimSpace(login)
	if login == "" {
		return "", errors.New("пустой логин")
	}

	// Логин кодируется base64: точка или слэш в логине сломали бы разбор по
	// разделителю, а такие логины в реальности встречаются.
	payload := base64.RawURLEncoding.EncodeToString([]byte(login)) +
		"." + strconv.FormatInt(now.Add(sessionTTL).Unix(), 10)

	return payload + "." + signPayload(payload, secret), nil
}

func signPayload(payload string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyToken(token string, secret []byte, now time.Time) (string, error) {
	if err := validateSecret(secret); err != nil {
		return "", err
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("неверный формат сессии")
	}

	payload := parts[0] + "." + parts[1]

	// Сравнение подписей постоянного времени: обычное == завершилось бы на
	// первом различии байта, и по времени ответа можно было бы подобрать
	// подпись побайтово.
	if subtle.ConstantTimeCompare([]byte(signPayload(payload, secret)), []byte(parts[2])) != 1 {
		return "", errors.New("подпись сессии не сходится")
	}

	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("в сессии не читается срок")
	}

	if now.Unix() > expires {
		return "", errors.New("срок сессии истёк")
	}

	login, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("в сессии не читается логин")
	}

	return string(login), nil
}

type ctxKey int

// ctxKeyLogin — типизированный ключ, а не строка. Строковым ключом в контексте
// может случайно затереть любой пакет, и это выглядело бы как «логин стал пустым
// без причины».
const ctxKeyLogin ctxKey = iota

func loginFromContext(ctx context.Context) string {
	login, _ := ctx.Value(ctxKeyLogin).(string)
	return login
}

func cookieSecure() bool {
	return os.Getenv("COOKIE_SECURE") == "true"
}

// requireDispatcher закрывает API: без сессии — 401.
//
// JSON, а не страница ошибки Go: этим маршрутом пользуется админка, и ответ
// должен быть в том же формате, что и остальные ошибки приложения.
func requireDispatcher(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login, err := sessionLogin(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Нужен вход диспетчера")
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyLogin, login)))
	}
}

// requireDispatcherPage закрывает страницу: без сессии — редирект на форму входа.
// Браузеру полезнее увидеть форму, чем JSON с кодом 401.
func requireDispatcherPage(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login, err := sessionLogin(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyLogin, login)))
	}
}

func sessionLogin(r *http.Request) (string, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", err
	}

	return verifyToken(cookie.Value, authSecret(), time.Now())
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(),
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный формат запроса")
		return
	}

	body.Login = strings.TrimSpace(body.Login)

	var hash string

	err := pool.QueryRow(r.Context(),
		`SELECT password_hash FROM dispatchers WHERE login = $1`, body.Login,
	).Scan(&hash)

	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("POST /auth/login: %v", err)
		writeError(w, http.StatusInternalServerError, "Не удалось войти")
		return
	}

	if errors.Is(err, pgx.ErrNoRows) {
		// Считаем пароль от настоящего хеша, чтобы время ответа не выдавало,
		// какие логины существуют.
		hash = dummyHash
	}

	if err := verifyPassword(hash, body.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "Неверный логин или пароль")
		return
	}

	token, err := signToken(body.Login, authSecret(), time.Now())
	if err != nil {
		log.Printf("POST /auth/login: %v", err)
		writeError(w, http.StatusInternalServerError, "Не удалось войти")
		return
	}

	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]string{"login": body.Login})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   cookieSecure(),
	})

	w.WriteHeader(http.StatusNoContent)
}

// ensureDispatcher создаёт первого диспетчера.
//
// DO NOTHING, а не DO UPDATE: пароль — это заготовка на первый запуск, а не
// постоянный источник правды. С DO UPDATE каждый перезапуск перетирал бы
// пароль, который диспетчер сменил сам, и «смена пароля» была бы иллюзией.
//
// Если в DISPATCHER_PASSWORD ничего нет, пароль не берётся из файла, а
// генерируется случайный и печатается в лог один раз. Заглушка вида
// «change-me» в .env опаснее отсутствия пароля: она одинаково подходит всем,
// кто её видел, и лежит в файле, который однако уедет в репозиторий.
func ensureDispatcher(ctx context.Context) error {
	login := strings.TrimSpace(os.Getenv("DISPATCHER_LOGIN"))

	if login == "" {
		log.Println("DISPATCHER_LOGIN не задан — новые диспетчеры не создаются")
		return nil
	}

	password := os.Getenv("DISPATCHER_PASSWORD")
	generated := false

	if password == "" {
		var err error
		if password, err = generatePassword(); err != nil {
			return err
		}

		generated = true
	}

	hash, err := hashPassword(password)
	if err != nil {
		return err
	}

	tag, err := pool.Exec(ctx, `
		INSERT INTO dispatchers (login, password_hash)
		VALUES ($1, $2)
		ON CONFLICT (login) DO NOTHING
	`, login, hash)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		log.Printf("диспетчер %q уже существует, пароль не применялся", login)
		return nil
	}

	if generated {
		log.Printf("создан диспетчер %q, пароль сгенерирован: %s", login, password)
		log.Println("сохраните пароль сейчас — он больше нигде не хранится, только хеш в базе")
		return nil
	}

	log.Printf("создан диспетчер %q из DISPATCHER_LOGIN", login)
	log.Println("пароль из .env — только заготовка для первого входа: смените его и уберите из .env")

	return nil
}

// generatePassword делает пароль из криптографически случайных байт.
//
// Такой пароль невозможно угадать перебором и невозможно прочитать из файла:
// единственное место, где он существует, — строка в логе при первом запуске.
func generatePassword() (string, error) {
	buf := make([]byte, 12)

	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("генерация пароля: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
