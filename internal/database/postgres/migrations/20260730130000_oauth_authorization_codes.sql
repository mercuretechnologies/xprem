-- +goose Up
-- +goose StatementBegin

-- One row per authorization code in flight: the single-use ticket the consent
-- screen mints and the token endpoint trades for a session. It freezes the
-- whole authorization context (client, redirect, PKCE challenge, user), so the
-- exchange can verify the request coming back is the one the user approved.
-- Rows live about a minute and are purged inline as new ones are minted.
CREATE TABLE oauth_authorization_codes (
    -- The code itself. It travels through the browser, so alone it is not
    -- enough to redeem: the exchange also demands the PKCE verifier, which
    -- never left the client.
    id UUID PRIMARY KEY,
    client_id UUID NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    scope TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    -- NULL until the code is exchanged; a second exchange finds it stamped
    -- and is refused.
    used_at TIMESTAMP WITH TIME ZONE
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oauth_authorization_codes;
-- +goose StatementEnd
