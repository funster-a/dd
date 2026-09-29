-- Карточка события (spec.md) и привязка секторов схемы к ценовым категориям.

-- +goose Up
ALTER TABLE events
    ADD COLUMN age_rating      text NOT NULL DEFAULT '0+'
                               CHECK (age_rating IN ('0+', '6+', '12+', '16+', '18+')),
    ADD COLUMN cover_image_key text,
    ADD COLUMN cover_video_key text;

-- Схема зала события принадлежит той же площадке, что и событие.
ALTER TABLE seat_maps ADD CONSTRAINT seat_maps_venue_id_id_key UNIQUE (venue_id, id);
ALTER TABLE events
    ADD CONSTRAINT events_seat_map_of_venue_fkey
    FOREIGN KEY (venue_id, seat_map_id) REFERENCES seat_maps (venue_id, id);

-- Какой ценовой категорией продаётся каждый сектор схемы. При публикации
-- по этой таблице выставляется price_category_id у сгенерированных мест.
CREATE TABLE event_section_prices (
    organizer_id      uuid NOT NULL,
    event_id          uuid NOT NULL,
    section           text NOT NULL,
    price_category_id uuid NOT NULL,
    PRIMARY KEY (event_id, section),
    FOREIGN KEY (organizer_id, event_id) REFERENCES events (organizer_id, id) ON DELETE CASCADE,
    FOREIGN KEY (event_id, price_category_id) REFERENCES price_categories (event_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE event_section_prices;
ALTER TABLE events DROP CONSTRAINT events_seat_map_of_venue_fkey;
ALTER TABLE seat_maps DROP CONSTRAINT seat_maps_venue_id_id_key;
ALTER TABLE events
    DROP COLUMN cover_video_key,
    DROP COLUMN cover_image_key,
    DROP COLUMN age_rating;
