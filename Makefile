# Команды разработки. Список с описаниями: make help.

# Переменные из .env (если он есть) действуют и здесь, и в docker compose,
# поэтому локальный запуск и контейнеры смотрят на одни и те же порты и пароли.
# Значения из .env сильнее переменных окружения; разовое переопределение —
# аргументом: make run HTTP_ADDR=:8081.
-include .env

POSTGRES_USER     ?= dd
POSTGRES_PASSWORD ?= dd
POSTGRES_DB       ?= dd
POSTGRES_PORT     ?= 5432
REDIS_PORT        ?= 6379
RABBITMQ_USER     ?= dd
RABBITMQ_PASSWORD ?= dd
RABBITMQ_PORT     ?= 5672

DATABASE_URL ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=disable
REDIS_ADDR   ?= localhost:$(REDIS_PORT)
RABBITMQ_URL ?= amqp://$(RABBITMQ_USER):$(RABBITMQ_PASSWORD)@localhost:$(RABBITMQ_PORT)/

# Экспортируем всё, включая переменные приложения из .env (HTTP_ADDR, LOG_LEVEL...),
# чтобы их видели процессы, запущенные через go run.
export

GOLANGCI_LINT_VERSION := v2.14.0
# goose закреплён в tools.mod, чтобы его зависимости не попадали в go.mod (ADR 002).
GOOSE_CMD := go tool -modfile=tools.mod goose
GOOSE := $(GOOSE_CMD) -dir migrations postgres "$(DATABASE_URL)"
INFRA := postgres redis rabbitmq prometheus grafana

.DEFAULT_GOAL := help

.PHONY: help up down infra-up build run run-worker test test-integration lint \
	migrate-up migrate-down migrate-reset migrate-status migrate-create

help: ## Показать список команд
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

up: ## Поднять весь проект в Docker: инфраструктура, миграции, api, worker
	docker compose up -d --build --wait

down: ## Остановить все контейнеры (данные в volumes сохраняются)
	docker compose down

infra-up: ## Поднять только инфраструктуру — для запуска api и worker через go run
	docker compose up -d --wait $(INFRA)

build: ## Собрать бинари api и worker в bin/
	go build -o bin/ ./cmd/api ./cmd/worker

run: ## Запустить api локально
	go run ./cmd/api

run-worker: ## Запустить worker локально
	go run ./cmd/worker

test: ## Юнит-тесты с детектором гонок
	go test -race ./...

test-integration: ## Все тесты, включая интеграционные (нужен make infra-up)
	DATABASE_TEST_URL="$(DATABASE_URL)" RABBITMQ_TEST_URL="$(RABBITMQ_URL)" go test -race -count=1 ./...

lint: ## Линтер той же версии, что в CI
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

migrate-up: ## Применить все новые миграции
	$(GOOSE) up

migrate-down: ## Откатить последнюю миграцию
	$(GOOSE) down

migrate-reset: ## Откатить все миграции
	$(GOOSE) reset

migrate-status: ## Показать статус миграций
	$(GOOSE) status

migrate-create: ## Создать миграцию: make migrate-create name=add_events
	@test -n "$(name)" || { echo "usage: make migrate-create name=<snake_case_name>"; exit 1; }
	$(GOOSE_CMD) -dir migrations -s create $(name) sql
