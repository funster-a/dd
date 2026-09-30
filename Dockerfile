# syntax=docker/dockerfile:1

# Один Dockerfile на все образы: api, worker, fakepsp (мок платёжного
# провайдера, ADR 012) и migrate (goose + миграции).
# Версия Go должна совпадать с go.mod; GOTOOLCHAIN=local не даст
# тихо скачать другую, а сломает сборку.
ARG GO_VERSION=1.27.1

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local

COPY go.mod go.sum tools.mod tools.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# goose собирается из tools.mod (ADR 002); драйверы других СУБД отключены
# тегами, нужен только postgres.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out/data && \
    go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/worker ./cmd/fakepsp && \
    go build -modfile=tools.mod -trimpath -ldflags="-s -w" \
      -tags "no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb" \
      -o /out/goose github.com/pressly/goose/v3/cmd/goose

FROM gcr.io/distroless/static-debian13:nonroot AS api
COPY --from=build /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]

FROM gcr.io/distroless/static-debian13:nonroot AS worker
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]

FROM gcr.io/distroless/static-debian13:nonroot AS fakepsp
COPY --from=build /out/fakepsp /fakepsp
# Каталог состояния мока: пустой том наследует владельца nonroot из образа.
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 8090
ENTRYPOINT ["/fakepsp"]

FROM gcr.io/distroless/static-debian13:nonroot AS migrate
COPY --from=build /out/goose /goose
COPY migrations /migrations
ENV GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=/migrations
ENTRYPOINT ["/goose"]
CMD ["up"]
