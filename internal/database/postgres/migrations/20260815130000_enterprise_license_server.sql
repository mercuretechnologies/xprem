-- +goose Up
-- +goose StatementBegin

-- Rebuilds enterprise_license for server-backed licensing; old offline keys
-- cannot be migrated, deployments re-attach from the dashboard.
DROP TABLE IF EXISTS enterprise_license;

CREATE TABLE enterprise_license (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    license_key TEXT NOT NULL,
    sealed_activation_secret TEXT NOT NULL,
    org_name TEXT NOT NULL,
    plan_code TEXT NOT NULL,
    subscription_start_at TIMESTAMP WITH TIME ZONE,
    subscription_end_at TIMESTAMP WITH TIME ZONE,
    subscription_renewal_at TIMESTAMP WITH TIME ZONE,
    activated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_validated_at TIMESTAMP WITH TIME ZONE,
    validation_failed_at TIMESTAMP WITH TIME ZONE,
    validation_error_code TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS enterprise_license;

CREATE TABLE enterprise_license (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    license_key TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementEnd
