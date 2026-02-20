-- name: CreateItem :one
INSERT INTO items (name, value) VALUES ($1, $2) RETURNING *;

-- name: GetItem :one
SELECT * FROM items WHERE id = $1;

-- name: ListItems :many
SELECT * FROM items ORDER BY created_at DESC;
