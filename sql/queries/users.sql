-- name: CreateUser :one
INSERT INTO users(id, created_at, updated_at, email, hashed_password)
VALUES(
    gen_random_uuid(),
    NOW(),
    NOW(),
    $1,
    $2
)
RETURNING *;

-- name: DeleteAllUsers :exec
DELETE FROM users;

-- name: SetUserPassword :exec
UPDATE users
SET hashed_password = $2, updated_at = NOW()
WHERE email = $1;

-- name: GetUserByEmail :one
SELECT * FROM users
WHERE email = $1;

