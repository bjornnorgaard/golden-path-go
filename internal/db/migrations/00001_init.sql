-- +goose Up
CREATE TABLE users (
    id           uuid PRIMARY KEY,
    email        varchar(256) NOT NULL,
    display_name varchar(256),
    given_name   varchar(256),
    family_name  varchar(256),
    avatar_url   varchar(2048)
);
CREATE UNIQUE INDEX ix_users_email ON users (email);

CREATE TABLE todos (
    id            uuid PRIMARY KEY,
    title         varchar(500) NOT NULL,
    due_by        timestamptz,
    is_complete   boolean NOT NULL,
    owner_user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE
);
CREATE INDEX ix_todos_owner_user_id ON todos (owner_user_id);

-- +goose Down
DROP TABLE todos;
DROP TABLE users;
