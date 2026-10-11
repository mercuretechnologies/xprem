-- +goose Up
CREATE TABLE build_cache_objects (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL,
    app_identifier_id UUID NOT NULL,
    namespace TEXT NOT NULL CHECK (namespace IN ('gradle', 'ccache')),
    cache_key TEXT NOT NULL CHECK (length(cache_key) BETWEEN 1 AND 256),
    size BIGINT NOT NULL CHECK (size BETWEEN 1 AND 536870912),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '1 day',
    published_at TIMESTAMPTZ,
    FOREIGN KEY (app_id, app_identifier_id) REFERENCES app_identifiers (app_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX build_cache_lookup ON build_cache_objects (app_id, app_identifier_id, namespace, cache_key) WHERE published_at IS NOT NULL;
CREATE INDEX build_cache_expiry ON build_cache_objects (expires_at);
CREATE INDEX build_cache_owner ON build_cache_objects (app_id, app_identifier_id);

-- Survives app/identifier deletion and retries bucket failures. Delay deletion
-- until upload URLs and in-flight downloads have expired.
CREATE TABLE build_cache_cleanup (
    id UUID PRIMARY KEY,
    app_id UUID NOT NULL,
    app_identifier_id UUID NOT NULL,
    namespace TEXT NOT NULL CHECK (namespace IN ('gradle', 'ccache')),
    size BIGINT NOT NULL CHECK (size BETWEEN 1 AND 536870912),
    due_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '20 minutes'
);
CREATE INDEX build_cache_cleanup_due ON build_cache_cleanup (due_at);
CREATE INDEX build_cache_cleanup_owner ON build_cache_cleanup (app_id, app_identifier_id);
-- +goose StatementBegin
CREATE FUNCTION enqueue_build_cache_cleanup() RETURNS trigger AS $$
BEGIN
    INSERT INTO build_cache_cleanup (id, app_id, app_identifier_id, namespace, size)
    VALUES (OLD.id, OLD.app_id, OLD.app_identifier_id, OLD.namespace, OLD.size);
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER trg_build_cache_cleanup AFTER DELETE ON build_cache_objects
FOR EACH ROW EXECUTE FUNCTION enqueue_build_cache_cleanup();

-- +goose Down
DROP TRIGGER IF EXISTS trg_build_cache_cleanup ON build_cache_objects;
DROP FUNCTION IF EXISTS enqueue_build_cache_cleanup;
DROP TABLE IF EXISTS build_cache_cleanup;
DROP TABLE IF EXISTS build_cache_objects;
