-- +goose Up
-- A short-lived record that a user deleted their account. The web app's JWT stays valid for up to
-- 5 minutes after the account is gone, and a PUT /me/profile with it would create the profiles row
-- again (data with no user). While a row here is recent, PUT /me/profile is refused.
-- The users live in the web app's database, so there is no foreign key. The row only has to
-- outlive the JWT; DELETE /me removes old ones. (userdata policy: retain, see internal/userdata.)
CREATE TABLE deleted_users (
    user_id    text        PRIMARY KEY,
    deleted_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
-- Destructive: never run this against production (migrations there only move forward).
DROP TABLE deleted_users;
