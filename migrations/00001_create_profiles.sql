-- +goose Up
-- One row per user. user_id is the JWT "sub" issued by the web app (Better Auth ids are
-- strings, not UUIDs). The users live in the web app's database, so there is no foreign key.
-- updated_at is not touched automatically: every UPDATE must set it (updated_at = now()).
CREATE TABLE profiles (
    user_id    text        PRIMARY KEY,
    bio        text        NOT NULL DEFAULT '' CHECK (char_length(bio) <= 1000),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Destructive: never run this against production (migrations there only move forward).
DROP TABLE profiles;
