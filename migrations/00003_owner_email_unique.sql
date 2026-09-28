-- Один вход на организатора (ADR 006): владелец входит по email, поэтому
-- email владельца должен однозначно указывать на одного организатора.

-- +goose Up
CREATE UNIQUE INDEX organizer_members_owner_email_key
    ON organizer_members (email) WHERE role = 'owner';

-- +goose Down
DROP INDEX organizer_members_owner_email_key;
