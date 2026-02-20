-- +goose Up
CREATE TABLE items (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE items;
