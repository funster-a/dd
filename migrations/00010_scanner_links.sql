-- Контроль входа: ссылки сканера и синхронизация сканирований (ADR 013).

-- +goose Up
-- Ссылка сканера даёт право сканировать билеты одного события. Хранится
-- только хэш токена: утечка базы не даёт доступ к сканеру.
CREATE TABLE scanner_links (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL,
    event_id     uuid        NOT NULL,
    name         text        NOT NULL CHECK (btrim(name) <> '' AND char_length(name) <= 100),
    token_hash   bytea       NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    UNIQUE (organizer_id, id),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id)
);
CREATE INDEX scanner_links_event_idx ON scanner_links (event_id);

-- Каждое сканирование приходит со своим id от устройства: повторная
-- синхронизация той же пачки ничего не удваивает.
ALTER TABLE ticket_scans
    ADD COLUMN scanner_link_id uuid NOT NULL,
    ADD COLUMN client_scan_id  text NOT NULL CHECK (btrim(client_scan_id) <> ''),
    ADD FOREIGN KEY (organizer_id, scanner_link_id) REFERENCES scanner_links (organizer_id, id);
CREATE UNIQUE INDEX ticket_scans_client_key ON ticket_scans (scanner_link_id, client_scan_id);

-- Засчитанный проход у билета один: при офлайн-синхронизации засчитывается
-- самое раннее сканирование, остальные — повторные попытки (spec.md).
CREATE UNIQUE INDEX ticket_scans_one_accepted_key ON ticket_scans (ticket_id) WHERE result = 'accepted';

-- +goose Down
DROP INDEX ticket_scans_one_accepted_key;
DROP INDEX ticket_scans_client_key;
ALTER TABLE ticket_scans DROP COLUMN client_scan_id, DROP COLUMN scanner_link_id;
DROP TABLE scanner_links;
