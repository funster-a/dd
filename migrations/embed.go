// Package migrations встраивает SQL-миграции в бинарь, чтобы тесты могли
// поднимать схему на временной базе. goose CLI этот файл пропускает:
// у него нет числового префикса версии.
package migrations

import "embed"

// FS содержит все SQL-миграции.
//
//go:embed *.sql
var FS embed.FS
