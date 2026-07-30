-- +goose Up
-- +goose StatementBegin

-- OAuth clients registered through dynamic client registration (RFC 7591),
-- the unauthenticated endpoint MCP clients use to introduce themselves before
-- their first authorization. Only public clients exist here: a client proves
-- nothing at registration and holds no secret afterwards, so a row is not a
-- credential. What the row pins down is the redirect allowlist: an
-- authorization code is only ever delivered to a URI this client declared at
-- registration time.
CREATE TABLE oauth_clients (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    redirect_uris TEXT[] NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS oauth_clients;
-- +goose StatementEnd
