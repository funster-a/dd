-- +goose Up
-- Версия строки места для оптимистичной блокировки (ADR 017). Колонка
-- заложена в схеме с начала (00002), но до сих пор её никто не вёл. Триггер
-- увеличивает её при любом изменении строки, кто бы ни писал: холд,
-- освобождение, продажа, возврат. Поэтому сравнение версии надёжно и не
-- зависит от того, помнит ли о ней каждый запрос.

-- +goose StatementBegin
CREATE FUNCTION event_seats_bump_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.version := OLD.version + 1;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER event_seats_version
    BEFORE UPDATE ON event_seats
    FOR EACH ROW EXECUTE FUNCTION event_seats_bump_version();

-- +goose Down
DROP TRIGGER event_seats_version ON event_seats;
DROP FUNCTION event_seats_bump_version();
