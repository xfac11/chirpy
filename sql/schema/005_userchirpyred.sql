-- +goose Up
ALTER TABLE users
ADD is_chirpy_red BOOLEAN DEFAULT false NOT NULL;

-- +goose Down
ALTER TABLE users
DROP is_chirpy_red;