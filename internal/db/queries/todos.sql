-- name: CreateTodo :one
INSERT INTO todos (id, title, due_by, is_complete, owner_user_id)
VALUES ($1, $2, $3, false, $4)
RETURNING *;

-- name: GetTodo :one
SELECT * FROM todos WHERE id = $1;

-- name: GetTodoForUpdate :one
SELECT * FROM todos WHERE id = $1 FOR UPDATE;

-- name: ListTodos :many
SELECT * FROM todos ORDER BY id LIMIT $1 OFFSET $2;

-- name: UpdateTodo :one
UPDATE todos
SET title = $2, due_by = $3, is_complete = $4
WHERE id = $1
RETURNING *;

-- name: ToggleTodo :one
UPDATE todos SET is_complete = NOT is_complete WHERE id = $1 RETURNING *;

-- name: DeleteTodo :execrows
DELETE FROM todos WHERE id = $1;

-- name: ListOverdueTodos :many
SELECT * FROM todos
WHERE NOT is_complete AND due_by IS NOT NULL AND due_by < $1
ORDER BY due_by;
