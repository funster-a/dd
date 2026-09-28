-- Базовая модель данных: арендаторы, покупатели, каталог, заказы, оплата,
-- возвраты, билеты. Решения и их причины — docs/adr/005-data-model.md.
--
-- Соглашения:
--   * ключи uuid v7 (упорядочены по времени, PostgreSQL 18: uuidv7());
--   * деньги — bigint в тиынах, время — timestamptz (UTC);
--   * статусы — text + CHECK, а не enum: так их проще менять миграциями;
--   * у данных арендатора есть organizer_id, а ссылки между ними идут
--     составными ключами (organizer_id, id), поэтому запись одного
--     организатора не может сослаться на запись другого.

-- +goose Up

CREATE TABLE organizers (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL CHECK (btrim(name) <> ''),
    slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Сотрудники организатора: владелец, менеджеры, контролёры на входе.
CREATE TABLE organizer_members (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL REFERENCES organizers (id),
    email        text        NOT NULL CHECK (email = lower(email) AND position('@' IN email) > 1),
    role         text        NOT NULL CHECK (role IN ('owner', 'manager', 'controller')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, email)
);

-- Покупатель общий для всех организаторов; идентичность — номер телефона
-- в формате E.164, подтверждённый SMS-кодом (коды живут в Redis).
CREATE TABLE buyers (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    phone      text        NOT NULL UNIQUE CHECK (phone ~ '^\+[1-9][0-9]{7,14}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE venues (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL REFERENCES organizers (id),
    name         text        NOT NULL CHECK (btrim(name) <> ''),
    address      text        NOT NULL DEFAULT '',
    -- IANA-зона для отображения на фронтенде; хранится и считается всё в UTC.
    timezone     text        NOT NULL DEFAULT 'Asia/Almaty',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id)
);

-- Схема зала из редактора — документ. При публикации события из неё
-- генерируются строки event_seats; дальнейшие правки схемы на уже
-- опубликованные события не влияют.
CREATE TABLE seat_maps (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL,
    venue_id     uuid        NOT NULL,
    name         text        NOT NULL CHECK (btrim(name) <> ''),
    layout       jsonb       NOT NULL CHECK (jsonb_typeof(layout) = 'object'),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    FOREIGN KEY (organizer_id, venue_id) REFERENCES venues (organizer_id, id)
);

CREATE TABLE events (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id          uuid        NOT NULL,
    venue_id              uuid        NOT NULL,
    seat_map_id           uuid        NOT NULL,
    slug                  text        NOT NULL CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title                 text        NOT NULL CHECK (btrim(title) <> ''),
    description           text        NOT NULL DEFAULT '',
    status                text        NOT NULL DEFAULT 'draft'
                                      CHECK (status IN ('draft', 'published', 'cancelled')),
    starts_at             timestamptz NOT NULL,
    ends_at               timestamptz NOT NULL,
    sales_start_at        timestamptz,
    sales_end_at          timestamptz,
    max_tickets_per_buyer integer     NOT NULL DEFAULT 10 CHECK (max_tickets_per_buyer > 0),
    -- Возврат возможен не позже чем за столько часов до начала (spec.md).
    refund_deadline_hours integer     NOT NULL DEFAULT 24 CHECK (refund_deadline_hours >= 0),
    published_at          timestamptz,
    cancelled_at          timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    UNIQUE (organizer_id, slug),
    FOREIGN KEY (organizer_id, venue_id) REFERENCES venues (organizer_id, id),
    FOREIGN KEY (organizer_id, seat_map_id) REFERENCES seat_maps (organizer_id, id),
    CHECK (ends_at > starts_at),
    CHECK (sales_end_at IS NULL OR sales_start_at IS NULL OR sales_end_at > sales_start_at),
    CHECK ((status = 'draft') = (published_at IS NULL) OR status = 'cancelled'),
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);

CREATE INDEX events_published_starts_at_idx ON events (starts_at) WHERE status = 'published';

CREATE TABLE price_categories (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL,
    event_id     uuid        NOT NULL,
    name         text        NOT NULL CHECK (btrim(name) <> ''),
    price_tiyn   bigint      NOT NULL CHECK (price_tiyn >= 0),
    currency     char(3)     NOT NULL DEFAULT 'KZT' CHECK (currency = upper(currency)),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    UNIQUE (event_id, id),
    UNIQUE (event_id, name),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id)
);

-- Место конкретного события. Секции без мест — виртуальные места
-- (kind = 'general', без ряда): для механики продажи разницы нет.
--
-- status, hold_* и version нужны стратегиям захвата места в PostgreSQL
-- (пессимистичная блокировка и версия строки). При стратегии на Redis
-- холд живёт только в Redis, а здесь место сразу переходит в 'sold'.
-- Окончательную защиту от двойной продажи даёт уникальный индекс на
-- tickets, а не этот статус.
CREATE TABLE event_seats (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id      uuid        NOT NULL,
    event_id          uuid        NOT NULL,
    price_category_id uuid        NOT NULL,
    kind              text        NOT NULL CHECK (kind IN ('seat', 'general')),
    section           text        NOT NULL CHECK (btrim(section) <> ''),
    row_label         text,
    seat_label        text        NOT NULL,
    status            text        NOT NULL DEFAULT 'available'
                                  CHECK (status IN ('available', 'held', 'sold')),
    hold_order_id     uuid,
    hold_expires_at   timestamptz,
    version           integer     NOT NULL DEFAULT 0,
    UNIQUE (organizer_id, id),
    UNIQUE (event_id, id),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id),
    FOREIGN KEY (event_id, price_category_id) REFERENCES price_categories (event_id, id),
    CHECK ((kind = 'seat') = (row_label IS NOT NULL)),
    CHECK ((status = 'held') = (hold_order_id IS NOT NULL AND hold_expires_at IS NOT NULL)),
    CHECK (status = 'held' OR (hold_order_id IS NULL AND hold_expires_at IS NULL))
);

-- NULLS NOT DISTINCT: у виртуальных мест row_label = NULL, и без этого
-- уникальность (секция, место) для них не проверялась бы.
CREATE UNIQUE INDEX event_seats_position_key
    ON event_seats (event_id, section, row_label, seat_label) NULLS NOT DISTINCT;
CREATE INDEX event_seats_event_status_idx ON event_seats (event_id, status);
CREATE INDEX event_seats_hold_expires_idx ON event_seats (hold_expires_at) WHERE status = 'held';

CREATE TABLE orders (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL,
    event_id     uuid        NOT NULL,
    buyer_id     uuid        NOT NULL REFERENCES buyers (id),
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'paid', 'expired', 'cancelled',
                                               'partially_refunded', 'refunded')),
    email        text        NOT NULL CHECK (position('@' IN email) > 1),
    total_tiyn   bigint      NOT NULL CHECK (total_tiyn >= 0),
    currency     char(3)     NOT NULL DEFAULT 'KZT',
    expires_at   timestamptz NOT NULL,
    paid_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    UNIQUE (event_id, id),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id),
    CHECK ((status IN ('paid', 'partially_refunded', 'refunded')) = (paid_at IS NOT NULL))
);

CREATE INDEX orders_buyer_event_idx ON orders (buyer_id, event_id);
CREATE INDEX orders_pending_expires_idx ON orders (expires_at) WHERE status = 'pending';

CREATE TABLE order_items (
    id            uuid   PRIMARY KEY DEFAULT uuidv7(),
    organizer_id  uuid   NOT NULL,
    event_id      uuid   NOT NULL,
    order_id      uuid   NOT NULL,
    event_seat_id uuid   NOT NULL,
    -- Цена на момент покупки: смена цены категории не меняет старые заказы.
    price_tiyn    bigint NOT NULL CHECK (price_tiyn >= 0),
    UNIQUE (organizer_id, id),
    UNIQUE (id, order_id, event_seat_id),
    UNIQUE (order_id, event_seat_id),
    FOREIGN KEY (event_id, order_id) REFERENCES orders (event_id, id),
    FOREIGN KEY (event_id, event_seat_id) REFERENCES event_seats (event_id, id),
    FOREIGN KEY (organizer_id, order_id) REFERENCES orders (organizer_id, id)
);

CREATE INDEX order_items_event_seat_idx ON order_items (event_seat_id);

-- Холд в PostgreSQL указывает на существующий заказ того же события.
ALTER TABLE event_seats
    ADD FOREIGN KEY (event_id, hold_order_id) REFERENCES orders (event_id, id);

CREATE TABLE payments (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id        uuid        NOT NULL,
    order_id            uuid        NOT NULL,
    status              text        NOT NULL DEFAULT 'created'
                                    CHECK (status IN ('created', 'pending', 'succeeded', 'failed')),
    amount_tiyn         bigint      NOT NULL CHECK (amount_tiyn > 0),
    currency            char(3)     NOT NULL DEFAULT 'KZT',
    provider            text        NOT NULL,
    provider_payment_id text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    UNIQUE (order_id, id),
    UNIQUE (provider, provider_payment_id),
    FOREIGN KEY (organizer_id, order_id) REFERENCES orders (organizer_id, id)
);

-- Попыток оплаты может быть несколько, успешная — только одна.
CREATE UNIQUE INDEX payments_one_success_per_order_key
    ON payments (order_id) WHERE status = 'succeeded';

-- Входящие уведомления шлюза. Первичный ключ отсекает повторную доставку
-- одного и того же уведомления (правило идемпотентности).
CREATE TABLE payment_webhook_events (
    provider          text        NOT NULL,
    provider_event_id text        NOT NULL,
    payload           jsonb       NOT NULL,
    received_at       timestamptz NOT NULL DEFAULT now(),
    processed_at      timestamptz,
    PRIMARY KEY (provider, provider_event_id)
);

CREATE TABLE tickets (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id  uuid        NOT NULL,
    event_id      uuid        NOT NULL,
    order_id      uuid        NOT NULL,
    order_item_id uuid        NOT NULL UNIQUE,
    event_seat_id uuid        NOT NULL,
    status        text        NOT NULL DEFAULT 'issued'
                              CHECK (status IN ('issued', 'used', 'revoked')),
    issued_at     timestamptz NOT NULL DEFAULT now(),
    used_at       timestamptz,
    revoked_at    timestamptz,
    UNIQUE (organizer_id, id),
    UNIQUE (order_id, id),
    FOREIGN KEY (event_id, order_id) REFERENCES orders (event_id, id),
    FOREIGN KEY (event_id, event_seat_id) REFERENCES event_seats (event_id, id),
    FOREIGN KEY (organizer_id, order_item_id) REFERENCES order_items (organizer_id, id),
    -- Место и заказ билета — те же, что у позиции заказа, по которой он выпущен.
    FOREIGN KEY (order_item_id, order_id, event_seat_id)
        REFERENCES order_items (id, order_id, event_seat_id),
    CHECK ((status = 'used') = (used_at IS NOT NULL)),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);

-- Главный инвариант (CLAUDE.md, правило 1): у места не больше одного
-- действующего билета — при любой конкурентности и любой стратегии захвата.
CREATE UNIQUE INDEX tickets_one_active_per_seat_key
    ON tickets (event_seat_id) WHERE status IN ('issued', 'used');
CREATE INDEX tickets_order_idx ON tickets (order_id);

CREATE TABLE refunds (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id       uuid        NOT NULL,
    payment_id         uuid        NOT NULL,
    order_id           uuid        NOT NULL,
    status             text        NOT NULL DEFAULT 'requested'
                                   CHECK (status IN ('requested', 'pending', 'succeeded', 'failed')),
    amount_tiyn        bigint      NOT NULL CHECK (amount_tiyn > 0),
    reason             text        NOT NULL CHECK (reason IN ('buyer_request', 'event_cancelled',
                                                              'late_payment', 'organizer_decision')),
    provider_refund_id text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organizer_id, id),
    UNIQUE (order_id, id),
    -- Возврат относится к платежу именно своего заказа.
    FOREIGN KEY (order_id, payment_id) REFERENCES payments (order_id, id),
    FOREIGN KEY (organizer_id, payment_id) REFERENCES payments (organizer_id, id),
    FOREIGN KEY (organizer_id, order_id) REFERENCES orders (organizer_id, id)
);

CREATE INDEX refunds_payment_idx ON refunds (payment_id);

-- Какие билеты входят в возврат. При возврате после поздней оплаты
-- билетов нет: заказ истёк, и их не выпускали.
CREATE TABLE refund_items (
    refund_id    uuid NOT NULL,
    ticket_id    uuid NOT NULL,
    organizer_id uuid NOT NULL,
    order_id     uuid NOT NULL,
    PRIMARY KEY (refund_id, ticket_id),
    -- Билет возвращается только в рамках возврата своего заказа.
    FOREIGN KEY (order_id, refund_id) REFERENCES refunds (order_id, id),
    FOREIGN KEY (order_id, ticket_id) REFERENCES tickets (order_id, id),
    FOREIGN KEY (organizer_id, refund_id) REFERENCES refunds (organizer_id, id)
);

-- Журнал сканирований, в том числе пришедших из офлайн-очереди сканера.
CREATE TABLE ticket_scans (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    organizer_id uuid        NOT NULL,
    ticket_id    uuid        NOT NULL,
    device_id    text        NOT NULL,
    scanned_at   timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    result       text        NOT NULL CHECK (result IN ('accepted', 'duplicate', 'revoked')),
    FOREIGN KEY (organizer_id, ticket_id) REFERENCES tickets (organizer_id, id)
);

CREATE INDEX ticket_scans_ticket_idx ON ticket_scans (ticket_id, scanned_at);

-- Ключи идемпотентности изменяющих эндпоинтов (правило 3): повторный
-- запрос с тем же ключом получает сохранённый ответ.
CREATE TABLE idempotency_keys (
    scope           text        NOT NULL,
    key             text        NOT NULL CHECK (length(key) BETWEEN 1 AND 255),
    request_hash    bytea       NOT NULL,
    response_status integer,
    response_body   jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz,
    PRIMARY KEY (scope, key),
    CHECK ((completed_at IS NULL) = (response_status IS NULL))
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- +goose Down

DROP TABLE idempotency_keys;
DROP TABLE ticket_scans;
DROP TABLE refund_items;
DROP TABLE refunds;
DROP TABLE tickets;
DROP TABLE payment_webhook_events;
DROP TABLE payments;
DROP TABLE order_items;
DROP TABLE event_seats;
DROP TABLE orders;
DROP TABLE price_categories;
DROP TABLE events;
DROP TABLE seat_maps;
DROP TABLE venues;
DROP TABLE buyers;
DROP TABLE organizer_members;
DROP TABLE organizers;
