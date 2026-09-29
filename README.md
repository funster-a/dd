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
| S3 (SeaweedFS) | http://localhost:8333 | `dd` / `dd-secret-key`, бакет `dd-media` открыт на чтение |
| PostgreSQL | `localhost:5432` | `dd` / `dd`, база `dd` |
| Redis | `localhost:6379` | без пароля |

Все порты открыты только на `127.0.0.1`. Порты и пароли меняются через `.env`: скопируйте `.env.example` в `.env` и поправьте нужное. Его читают и docker compose, и Makefile.

- **Порты** можно менять когда угодно.
- **Логины, пароли и имя базы** PostgreSQL, RabbitMQ и Grafana образы применяют только при первом создании volume. После их смены выполните `docker compose down -v` — это удалит локальные данные.
- **Пароли подставляются в строки подключения как есть**, поэтому используйте только буквы, цифры и `-`, `_`, `.`, `~`. Makefile читает `.env` по правилам make: без кавычек, без комментариев в конце строки и без `$` и `#` в значениях.

## Локальная разработка

Инфраструктура работает в Docker, api и worker запускаются через `go run`. Так быстрее пересобирать и можно подключить отладчик.

```sh
make infra-up        # только PostgreSQL, Redis, RabbitMQ, SeaweedFS, Prometheus, Grafana
make migrate-up      # применить миграции
make run             # api на :8080
make run-worker      # worker, в другом терминале
```

Если api из Docker уже запущен, он занимает порт 8080. Остановите его (`docker compose stop api`) или запустите локальный на другом порту: `make run HTTP_ADDR=:8081`.

Разовые переопределения передавайте аргументом make (`make run LOG_LEVEL=debug`), а не префиксом перед командой. Значения из `.env` для make сильнее переменных окружения.

### Команды

`make help` выводит полный список.

| Команда | Что делает |
|---|---|
| `make up` / `make down` | поднять или остановить весь проект в Docker |
| `make infra-up` | поднять только инфраструктуру |
| `make run` / `make run-worker` | запустить api или worker локально |
| `make build` | собрать бинари в `bin/` |
| `make test` | юнит-тесты с детектором гонок |
| `make test-integration` | все тесты, включая интеграционные с PostgreSQL и RabbitMQ (нужен `make infra-up`) |
| `make lint` | golangci-lint той же версии, что в CI |
| `make sqlc` | сгенерировать Go-код запросов из `queries.sql` |
| `make migrate-up` | применить все новые миграции |
| `make migrate-down` | откатить последнюю миграцию |
| `make migrate-reset` | откатить все миграции |
| `make migrate-status` | статус миграций |
| `make migrate-create name=add_events` | создать новую миграцию |

`make test` и `make test-integration` запускаются с `-race`. На Linux детектору гонок нужен C-компилятор (gcc), на macOS — не нужен.

### Переменные окружения api и worker

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `HTTP_ADDR` | `:8080` | адрес HTTP-сервера api |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `15s` | общий бюджет корректной остановки |
| `DATABASE_URL` | `postgres://dd:dd@localhost:5432/dd?sslmode=disable` | PostgreSQL |
| `REDIS_ADDR` | `localhost:6379` | Redis |
| `RABBITMQ_URL` | `amqp://dd:dd@localhost:5672/` | RabbitMQ |

Значения по умолчанию совпадают с `docker-compose.yml`. Все переменные проверяются при старте (`DATABASE_URL` — в api, который им пользуется). Неверное значение, например `LOG_LEVEL=loud` или `REDIS_ADDR=redis` без порта, останавливает процесс с понятной ошибкой. Пароли в текст ошибок не попадают.

## API

Все эндпоинты — под `/v1`, ошибки в формате `{"error": {"code": "...", "message": "..."}}` (ADR 007). Вход без паролей, по одноразовому коду (ADR 006). В разработке код не отправляется, а пишется в лог api.

Вход покупателя:

```sh
curl -X POST localhost:8080/v1/auth/codes -d '{"kind":"buyer","phone":"+77001234567"}'
docker compose logs api | grep one-time    # код из лога
curl -X POST localhost:8080/v1/auth/sessions -d '{"kind":"buyer","phone":"+77001234567","code":"123456"}'
curl localhost:8080/v1/me -H "Authorization: Bearer <token>"
curl -X DELETE localhost:8080/v1/auth/session -H "Authorization: Bearer <token>"
```

Организатор входит так же, но с `"kind":"organizer","email":"..."`, администратор платформы — с `"kind":"admin"`. Его email должен быть в `ADMIN_EMAILS`.

Администратор заводит организатора. После этого владелец может войти по своему email:

```sh
curl -X POST localhost:8080/v1/admin/organizers \
  -H "Authorization: Bearer <admin-token>" -H "Idempotency-Key: $(uuidgen)" \
  -d '{"name":"Stand-up Club","slug":"standup-club","owner_email":"owner@example.com"}'
```

Кабинет организатора (`/v1/organizer`, организатор берётся из сессии и видит только свои данные):

| Метод и путь | Что делает |
|---|---|
| `GET/POST /venues`, `GET/PUT /venues/{id}` | площадки: название, адрес, часовой пояс, координаты |
| `GET/POST /venues/{id}/seat-maps`, `GET /seat-maps/{id}` | схемы залов |
| `GET/POST /events`, `GET/PUT /events/{id}` | события (черновик меняется целиком) |
| `GET/PUT /events/{id}/prices` | ценовые категории и их секторы |
| `POST /events/{id}/media/uploads`, `PUT /events/{id}/media` | ссылка на загрузку обложки и её прикрепление |
| `POST /events/{id}/publish` | публикация: проверка готовности и генерация мест |

Схема зала — это сектора с рядами и местами и входные зоны с вместимостью:

```json
{"name": "Open air", "layout": {"sections": [
  {"name": "Фан-зона", "kind": "general", "capacity": 1500},
  {"name": "VIP", "kind": "seat", "rows": [{"label": "1", "seats": [{"label": "1", "x": 10, "y": 20}]}]}
]}}
```

Публичная страница события — без входа, только опубликованные события:

```sh
curl localhost:8080/v1/public/events/standup-club/<event-slug>
```

В ответе карточка, площадка, обложки, цены и схема зала. Ответ кэшируется в Redis на 10 минут. Одновременные промахи кэша идут в базу одним запросом. Смена обложки сбрасывает кэш сразу.

Все изменяющие запросы, кроме входа, требуют заголовок `Idempotency-Key`. Повтор с тем же ключом возвращает прежний ответ и ничего не создаёт заново (ADR 008).

## Как устроено

```
cmd/api/          HTTP-сервер: chi, /healthz, /readyz, graceful shutdown
cmd/worker/       потребитель RabbitMQ: очередь dd.test, переподключение, graceful shutdown
internal/catalog/ события, залы, схемы мест        (пока пусто)
internal/booking/ холды, брони, статусы заказа     (пока пусто)
internal/payment/ платёжный шлюз, вебхуки, возвраты (пока пусто)
internal/ticket/  QR, валидация, отчёты            (пока пусто)
internal/identity/ вход по одноразовому коду, сессии
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
- **Test** — миграции против PostgreSQL: `up`, `reset` с проверкой, что в базе ничего не осталось, снова `up`; затем все тесты с `-race`, включая тесты инвариантов схемы и интеграционные с RabbitMQ;
- **Compose** — `docker compose up --build --wait` с нуля, проверка api и доставки сообщения до worker.

Интеграционные тесты запускаются, только если заданы переменные `DATABASE_TEST_URL` (PostgreSQL) и `RABBITMQ_TEST_URL`, иначе пропускаются. `make test-integration` задаёт их сама. Тесты базы создают для себя временную базу `dd_test_*`, применяют к ней миграции и удаляют её после теста, поэтому рабочая база не засоряется.

Главный инвариант — место не продаётся дважды — проверяется прямо на схеме: 50 транзакций одновременно выпускают билет на одно место, и выпуск удаётся ровно у одной (`migrations/schema_test.go`).

## Если что-то не работает

- **`docker compose up` падает с `429 Too Many Requests`.** Это лимит анонимных скачиваний Docker Hub. Выполните `docker login` или подождите.
- **Порт уже занят.** Поменяйте соответствующий `*_PORT` в `.env`.
- **`/readyz` отвечает 503.** Лог api (`docker compose logs api`) показывает, какая зависимость недоступна и почему.
