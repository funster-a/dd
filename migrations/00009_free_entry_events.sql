-- События со свободным входом: без билетов и без схемы зала (ADR 013).

-- +goose Up
ALTER TABLE events
    ADD COLUMN admission text NOT NULL DEFAULT 'ticketed'
        CHECK (admission IN ('ticketed', 'free_entry')),
    ALTER COLUMN seat_map_id DROP NOT NULL,
    -- Билеты продаются только по схеме зала; у свободного входа её нет.
    ADD CONSTRAINT events_seat_map_matches_admission
        CHECK ((admission = 'ticketed') = (seat_map_id IS NOT NULL));

-- +goose Down
-- Откат возможен, только если событий со свободным входом нет.
ALTER TABLE events
    DROP CONSTRAINT events_seat_map_matches_admission,
    ALTER COLUMN seat_map_id SET NOT NULL,
    DROP COLUMN admission;
