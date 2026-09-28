-- Площадка — любое место проведения (spec.md): у неё есть координаты для карты.

-- +goose Up
ALTER TABLE venues
    ADD COLUMN latitude  double precision CHECK (latitude BETWEEN -90 AND 90),
    ADD COLUMN longitude double precision CHECK (longitude BETWEEN -180 AND 180),
    ADD CONSTRAINT venues_coordinates_together CHECK ((latitude IS NULL) = (longitude IS NULL));

-- +goose Down
ALTER TABLE venues
    DROP CONSTRAINT venues_coordinates_together,
    DROP COLUMN longitude,
    DROP COLUMN latitude;
