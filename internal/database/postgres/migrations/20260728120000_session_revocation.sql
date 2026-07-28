-- +goose Up
-- +goose StatementBegin

-- The account's security generation. Every dashboard JWT carries the value it
-- was minted with, and both the per-request check and the refresh path compare
-- that claim against this column: a single UPDATE kills every access and
-- refresh token the account holds, with no token blacklist to maintain and no
-- extra read (the request already resolves the row to check `enabled`).
--
-- Bumped when the account's security state changes under its own sessions:
-- disabled, demoted, password changed. Deleting the account needs no bump, the
-- row is gone and the per-request lookup fails.
ALTER TABLE users ADD COLUMN session_version INTEGER NOT NULL DEFAULT 0;

-- One row per refresh token ever issued to a live sign-in, so refreshing can
-- ROTATE instead of handing the same 7-day credential back: the presented
-- token is consumed (used_at) and a successor is issued in its place. A stolen
-- refresh token is therefore only worth one use, and the theft is detectable:
-- a token that comes back after it was spent means two parties hold the chain,
-- so the whole family goes.
--
-- Control-plane only. Stateless deployments (ADMIN_EMAIL/ADMIN_PASSWORD) have
-- no database and keep the unrotated refresh they always had.
CREATE TABLE refresh_tokens (
    -- The jti claim of the refresh JWT. The token is signed, so possession of
    -- it is what proves the caller owns this row; the id itself is not a
    -- secret and is safe to log.
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The sign-in this token descends from. Rotation carries it over, so one
    -- DELETE revokes a leaked chain and only that chain, leaving the account's
    -- other devices signed in.
    family_id UUID NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    -- NULL while this is the family's live token; stamped when it is rotated.
    -- A second attempt to consume a stamped row is the replay signal.
    used_at TIMESTAMP WITH TIME ZONE,
    -- The token this one was rotated into, written in the same transaction as
    -- used_at. It exists for the replay grace: a client whose parallel
    -- requests all refresh at once presents the same token twice, and the
    -- second call is answered with the successor ALREADY issued rather than
    -- with a second live token. Minting a second one would fork the chain into
    -- two unconsumed tokens, after which neither holder ever presents a
    -- consumed token again and the family becomes undetectable.
    replaced_by UUID
);

-- Revoking a family walks it by family_id.
CREATE INDEX idx_refresh_tokens_family ON refresh_tokens (family_id);
-- Issuing a token purges that user's expired rows in the same request, which
-- is what keeps the table bounded without a background job.
CREATE INDEX idx_refresh_tokens_user ON refresh_tokens (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS refresh_tokens;
ALTER TABLE users DROP COLUMN IF EXISTS session_version;
-- +goose StatementEnd
