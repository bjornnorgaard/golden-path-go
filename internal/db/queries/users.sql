-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id LIMIT $1 OFFSET $2;

-- name: UpdateUser :one
UPDATE users
SET display_name = $2, given_name = $3, family_name = $4, avatar_url = $5
WHERE id = $1
RETURNING *;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: UpsertUserByEmail :one
INSERT INTO users (id, email, display_name, given_name, family_name, avatar_url)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
RETURNING *;
