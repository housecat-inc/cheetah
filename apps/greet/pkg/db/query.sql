-- name: ListGreetings :many
SELECT * FROM greetings ORDER BY created_at DESC LIMIT 20;

-- name: CreateGreeting :one
INSERT INTO greetings (name, message, emoji) VALUES ($1, $2, $3) RETURNING *;
