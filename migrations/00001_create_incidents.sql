-- +goose Up

-- environment and priority are fixed, closed sets, so native enum types fit.
CREATE TYPE environment_enum AS ENUM ('dev', 'staging', 'prod');
CREATE TYPE priority_enum AS ENUM ('critical', 'high', 'medium', 'low');

-- One row per distinct incident, not per occurrence: a repeat of the same
-- error within the dedup window only bumps occurrence_count / last_seen_at.
CREATE TABLE incidents (
    id                 uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    service            text             NOT NULL,
    environment        environment_enum NOT NULL,
    timestamp          timestamptz      NOT NULL,
    raw_text_hash      varchar(64)      NOT NULL,

    -- category is the one taxonomy likely to grow, and a native enum is
    -- painful to change (ALTER TYPE ... ADD VALUE can't be used in the same
    -- transaction it runs in; values can't be renamed or removed). A CHECK
    -- constraint gives the same guarantee and is a plain drop/add to evolve.
    category           text             NOT NULL
        CONSTRAINT ck_incidents_category CHECK (category IN (
            'database_timeout', 'null_pointer', 'rate_limit_exceeded', 'auth_failure',
            'network_partial_failure', 'validation_error', 'unknown'
        )),
    root_cause_summary varchar(300)     NOT NULL,
    priority           priority_enum    NOT NULL,
    priority_reasoning varchar(200)     NOT NULL,
    confidence         double precision NOT NULL
        CONSTRAINT ck_incidents_confidence CHECK (confidence BETWEEN 0 AND 1),
    needs_human_review boolean          NOT NULL,

    occurrence_count   integer          NOT NULL DEFAULT 1,
    llm_retry_count    integer          NOT NULL DEFAULT 0,
    llm_latency_ms     integer,

    first_seen_at      timestamptz      NOT NULL DEFAULT now(),
    last_seen_at       timestamptz      NOT NULL DEFAULT now()
);

-- Serves the dedup lookup exactly: WHERE raw_text_hash = $1 AND last_seen_at >= $2
-- ORDER BY last_seen_at DESC LIMIT 1. raw_text_hash is deliberately not unique:
-- the same hash legitimately gets a new row once the dedup window has passed.
CREATE INDEX ix_incidents_hash_last_seen ON incidents (raw_text_hash, last_seen_at DESC);

-- +goose Down
DROP TABLE incidents;
DROP TYPE priority_enum;
DROP TYPE environment_enum;
