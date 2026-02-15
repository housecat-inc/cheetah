-- +goose Up
ALTER TABLE greetings ADD COLUMN emoji TEXT NOT NULL DEFAULT '👋';

-- +goose Down
ALTER TABLE greetings DROP COLUMN emoji;
