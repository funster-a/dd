-- Цена по рядам (ADR 025): часть сектора продаётся другой категорией,
-- например первые три ряда у беговой дорожки дешевле остальных. Диапазон
-- рядов — от row_from до row_to включительно в порядке схемы зала. Сектор
-- продаётся либо целиком (event_section_prices), либо по диапазонам рядов,
-- которые покрывают его ряды ровно один раз; это проверяет сервис по схеме.

-- +goose Up
CREATE TABLE event_row_prices (
    organizer_id      uuid NOT NULL,
    event_id          uuid NOT NULL,
    section           text NOT NULL,
    row_from          text NOT NULL,
    row_to            text NOT NULL,
    price_category_id uuid NOT NULL,
    PRIMARY KEY (event_id, section, row_from),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id) ON DELETE CASCADE,
    FOREIGN KEY (event_id, price_category_id) REFERENCES price_categories (event_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE event_row_prices;
