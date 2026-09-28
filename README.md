# dd — SaaS для продажи билетов

Платформа для организаторов мероприятий: организатор заводит событие и схему зала и продаёт билеты со своего сайта. Дипломный проект.

Сейчас в репозитории **каркас** без бизнес-логики: HTTP-сервер, потребитель очереди, инфраструктура, миграции и CI.

- Правила работы и стек — [`CLAUDE.md`](CLAUDE.md)
- Спецификация — [`docs/spec.md`](docs/spec.md)
- Архитектурные решения — [`docs/adr/`](docs/adr/)

## Что нужно установить

| Инструмент | Версия | Зачем |
|---|---|---|
| Docker с Compose v2 | Docker 24+, Compose 2.24+ | инфраструктура и запуск проекта |
| Go | 1.27.1 | локальная разработка; с Go 1.21+ нужная версия скачается сама по `go.mod` |
| make | любая | команды разработки |

Для одного только запуска в Docker Go и make не нужны.

## Быстрый старт: весь проект в Docker

```sh
git clone https://github.com/funster-a/dd.git
cd dd
docker compose up -d --build --wait
```

Команда собирает образы и поднимает PostgreSQL, Redis, RabbitMQ, Prometheus и Grafana. Затем применяет миграции и запускает api и worker. Она завершается, когда все сервисы здоровы.

Проверка:

```sh
curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"checks":{"postgres":"ok","rabbitmq":"ok","redis":"ok"},"status":"ok"}
```

Отправить сообщение в тестовую очередь worker и увидеть его в логе:

```sh
curl -u dd:dd -H 'content-type: application/json' \
  -X POST http://localhost:15672/api/exchanges/%2F/amq.default/publish \
  -d '{"routing_key":"dd.test","payload":"hello","payload_encoding":"string"}'
docker compose logs worker
```

Остановить: `docker compose down`. Удалить вместе с данными: `docker compose down -v`.

### Адреса

| Сервис | Адрес | Доступ |
|---|---|---|
| api | http://localhost:8080 | — |
| RabbitMQ, панель управления | http://localhost:15672 | `dd` / `dd` |
| Prometheus | http://localhost:9090 | — |
| Grafana | http://localhost:3000 | `admin` / `admin`, источник Prometheus подключён |
| PostgreSQL | `localhost:5432` | `dd` / `dd`, база `dd` |
| Redis | `localhost:6379` | без пароля |

Все порты открыты только на `127.0.0.1`. Порты и пароли меняются через `.env`: скопируйте `.env.example` в `.env` и поправьте нужное. Его читают и docker compose, и Makefile.

## Локальная разработка

Инфраструктура работает в Docker, api и worker запускаются через `go run`. Так быстрее пересобирать и можно подключить отладчик.

```sh
make infra-up        # только PostgreSQL, Redis, RabbitMQ, Prometheus, Grafana
make migrate-up      # применить миграции
make run             # api на :8080
make run-worker      # worker, в другом терминале
```

Если api из Docker уже запущен, он занимает порт 8080. Остановите его (`docker compose stop api`) или запустите локальный на другом порту: `HTTP_ADDR=:8081 make run`.

### Команды

`make help` выводит полный список.

| Команда | Что делает |
|---|---|
| `make up` / `make down` | поднять или остановить весь проект в Docker |
| `make infra-up` | поднять только инфраструктуру |
| `make run` / `make run-worker` | запустить api или worker локально |
| `make build` | собрать бинари в `bin/` |
| `make test` | юнит-тесты с детектором гонок |
| `make test-integration` | все тесты, включая интеграционные с RabbitMQ (нужен `make infra-up`) |
| `make lint` | golangci-lint той же версии, что в CI |
| `make migrate-up` | применить все новые миграции |
| `make migrate-down` | откатить последнюю миграцию |
| `make migrate-reset` | откатить все миграции |
| `make migrate-status` | статус миграций |
| `make migrate-create name=add_events` | создать новую миграцию |

`make test` и `make test-integration` запускаются с `-race`, а детектору гонок нужен C-компилятор: на Linux это gcc, на macOS — Xcode Command Line Tools.

### Переменные окружения api и worker

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `HTTP_ADDR` | `:8080` | адрес HTTP-сервера api |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `15s` | общий бюджет корректной остановки |
| `DATABASE_URL` | `postgres://dd:dd@localhost:5432/dd?sslmode=disable` | PostgreSQL |
| `REDIS_ADDR` | `localhost:6379` | Redis |
| `RABBITMQ_URL` | `amqp://dd:dd@localhost:5672/` | RabbitMQ |

Значения по умолчанию совпадают с `docker-compose.yml`. Неверное значение, например `LOG_LEVEL=loud`, останавливает процесс при старте с понятной ошибкой.

## Как устроено

```
cmd/api/          HTTP-сервер: chi, /healthz, /readyz, graceful shutdown
cmd/worker/       потребитель RabbitMQ: очередь dd.test, переподключение, graceful shutdown
internal/catalog/ события, залы, схемы мест        (пока пусто)
internal/booking/ холды, брони, статусы заказа     (пока пусто)
internal/payment/ платёжный шлюз, вебхуки, возвраты (пока пусто)
internal/ticket/  QR, валидация, отчёты            (пока пусто)
internal/platform/ общий код: config, db, redis, mq, httpx, observability
migrations/       goose-миграции
docs/             спецификация и ADR
web/, loadtest/   фронтенд на Vue и сценарии k6 (появятся позже)
```

- **Логи** пишутся в JSON через `log/slog`. У каждой строки есть поле `service`, у строк запроса — `request_id` (заголовок `X-Request-ID`).
- **`/healthz`** отвечает 200, пока процесс жив. **`/readyz`** проверяет PostgreSQL, Redis и RabbitMQ и отвечает 200 или 503. Подробности — в ADR 003.
- **Границы модулей** проверяет линтер: доменные модули не импортируют друг друга, `platform` не зависит от доменных модулей. Подробности — в ADR 004.

## Тесты и CI

GitHub Actions ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) запускается на каждый push и выполняет:

- **Build** — `go mod tidy -diff`, сборка;
- **Lint** — golangci-lint v2.14.0;
- **Test** — миграции `up`, `reset`, `up`, затем все тесты с `-race` против PostgreSQL и RabbitMQ;
- **Compose** — `docker compose up --build --wait` с нуля, проверка api и доставки сообщения до worker.

Интеграционные тесты RabbitMQ запускаются, только если задана переменная `RABBITMQ_TEST_URL`, иначе пропускаются. `make test-integration` задаёт её сама.

## Если что-то не работает

- **`docker compose up` падает с `429 Too Many Requests`.** Это лимит анонимных скачиваний Docker Hub. Выполните `docker login` или подождите.
- **Порт уже занят.** Поменяйте соответствующий `*_PORT` в `.env`.
- **`/readyz` отвечает 503.** Лог api (`docker compose logs api`) показывает, какая зависимость недоступна и почему.
