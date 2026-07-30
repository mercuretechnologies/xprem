-- +goose Up
-- +goose StatementBegin

-- Per-key branch access rules (Enterprise Edition, ee/apikeyrestrictions).
-- They replace api_keys.can_access_protected_branches, which could only say
-- "this key stays off the branches an admin marked": one bit for the key, one
-- bit for the branch, and no way to say which branches a CI token actually
-- publishes to.

CREATE TABLE api_key_branch_rules (
    id BIGSERIAL PRIMARY KEY,
    api_key_id BIGINT NOT NULL,
    -- A branch name, or a name with "*" standing for any run of characters
    -- ("pr-*", "*-eu", "*"). Branch names cannot contain "*", so a pattern is
    -- never ambiguous with a literal.
    -- Sized like branches.name rather than like validation.Name's 128-char cap:
    -- a deployment that predates that cap can hold longer branch names, and a
    -- rule has to be able to name what exists.
    pattern VARCHAR(255) NOT NULL,
    -- read | publish | rollback, validated in Go against the catalog before
    -- it gets here. Both writes imply read.
    actions TEXT[] NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_api_key_branch_rules_api_key FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE,
    -- One rule per pattern: two rules on the same pattern would have to be
    -- unioned mentally by whoever reads the key. Its index also serves the
    -- enforcement read and the cascade, both keyed on api_key_id.
    CONSTRAINT uq_api_key_branch_rule UNIQUE (api_key_id, pattern)
);

-- Data migration, from one bit to rules.
--
-- A key that COULD access protected branches reached every branch of its app,
-- so it gets no rule at all: an empty rule list is exactly "every branch".
--
-- A key that could NOT gets one rule per branch it could actually reach TODAY.
-- That is narrower than what it held: the old flag denied only the branches
-- flagged protected, and an unknown branch was not protected, so such a key
-- could also publish to branches created after the fact. A rule list is a
-- snapshot and cannot express that; an admin who needs it writes a pattern.
--
-- The EXISTS clause is what keeps community deployments untouched: the column
-- defaults to FALSE for everyone, and setting a branch protected is
-- license-gated, so an app with no protected branch never enforced anything
-- and must not come out of this migration scoped to the branches that
-- happened to exist today.
--
-- Branch names containing "*" are skipped rather than turned into a rule: the
-- name would read as a wildcard and widen the key instead of describing it.
-- validation.Name refuses such names now, so only rows predating it qualify.
INSERT INTO api_key_branch_rules (api_key_id, pattern, actions)
SELECT k.id, b.name, ARRAY['read', 'publish', 'rollback']
FROM api_keys k
JOIN branches b ON b.app_id = k.app_id AND NOT b.protected AND position('*' in b.name) = 0
WHERE k.revoked_at IS NULL
  AND NOT k.can_access_protected_branches
  AND EXISTS (SELECT 1 FROM branches p WHERE p.app_id = k.app_id AND p.protected);

ALTER TABLE api_keys DROP COLUMN can_access_protected_branches;

-- branches.protected STAYS, with a narrower job. It no longer takes any part in
-- authorizing an API key, which is what the rules above are for; what is left
-- is the guard that refuses to delete a protected branch, admins included.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Lossy on purpose: rules are richer than the bit they replaced, so going back
-- restores the column at its default and drops what cannot be expressed. Every
-- key comes back at FALSE, the restrictive end, including those that used to
-- hold TRUE.
ALTER TABLE api_keys ADD COLUMN can_access_protected_branches BOOLEAN NOT NULL DEFAULT FALSE;
DROP TABLE IF EXISTS api_key_branch_rules;

-- +goose StatementEnd
