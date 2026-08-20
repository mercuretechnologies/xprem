-- +goose Up

-- The deployment's permanent identity, minted once on first boot. The
-- singleton key caps the table at one row, and the triggers below make that
-- row append-only for every role short of a superuser dropping the trigger.
CREATE TABLE server_instance (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    id UUID NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementBegin
CREATE FUNCTION server_instance_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'server_instance is immutable';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_server_instance_immutable
BEFORE UPDATE OR DELETE ON server_instance
FOR EACH ROW EXECUTE FUNCTION server_instance_immutable();

CREATE TRIGGER trg_server_instance_no_truncate
BEFORE TRUNCATE ON server_instance
FOR EACH STATEMENT EXECUTE FUNCTION server_instance_immutable();

-- +goose Down

DROP TABLE server_instance;
DROP FUNCTION server_instance_immutable;
