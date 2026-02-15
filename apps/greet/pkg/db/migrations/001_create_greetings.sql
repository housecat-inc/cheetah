-- +goose Up
CREATE TABLE greetings (
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    emoji TEXT NOT NULL DEFAULT '❤️',
    id SERIAL PRIMARY KEY,
    message TEXT NOT NULL,
    name TEXT NOT NULL
);

-- +goose Down
DROP TABLE greetings;
