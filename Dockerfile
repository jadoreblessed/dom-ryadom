FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates

# API MAX отдаётся сертификатом, выпущенным «Russian Trusted Sub CA»
# (Минцифры России). В alpine этого корня нет, и TLS-рукопожатие падает с
# «certificate signed by unknown authority» — то есть бот не может написать
# жителю вообще. Документация MAX об этом предупреждает.
#
# Это осознанный пин: файл взят из цепочки, которую сам сервер MAX предъявляет
# при подключении, и проверен по отпечатку. Пока он не выводится в репозиторий
# на ftp.gu-st.ru, официального корня «Russian Trusted Root CA» у нас нет.
# Смена сертификата на стороне MAX даст громкую ошибку в логах при отправке,
# а не тихую: сообщения просто перестанут уходить.
COPY deploy/max-root-ca.pem /usr/local/share/ca-certificates/max-root-ca.crt
RUN update-ca-certificates

WORKDIR /app
COPY --from=build /out/app ./app
COPY web ./web
EXPOSE 8080
ENTRYPOINT ["/app/app"]
