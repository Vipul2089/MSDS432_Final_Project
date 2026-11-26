CREATE TABLE log_sources (
    source_id   smallint    PRIMARY KEY,
    source_name varchar(20) NOT NULL UNIQUE,
    format      varchar(10) NOT NULL CHECK (format IN ('csv', 'json')),
    is_active   boolean     NOT NULL DEFAULT true
);

CREATE TABLE tier_policies (
    policy_name     varchar(20) PRIMARY KEY,
    bucket_size     interval    NOT NULL,
    hot_after       interval    NOT NULL,
    warm_after      interval    NOT NULL,
    retain_for      interval    NOT NULL,
    late_window     interval    NOT NULL,
    purge_grace     interval    NOT NULL,
    target_shard_gb int         NOT NULL DEFAULT 25,
    is_active       boolean     NOT NULL DEFAULT false,
    CHECK (hot_after < warm_after AND warm_after < retain_for)
);

-- At most one policy can be active at a time.
CREATE UNIQUE INDEX tier_policies_one_active ON tier_policies (is_active) WHERE is_active;

-- The catalog: where each time bucket of data can be read right now.
CREATE TABLE data_partitions (
    partition_id  bigserial     PRIMARY KEY,
    bucket_start  timestamptz   NOT NULL UNIQUE,
    bucket_end    timestamptz   NOT NULL,
    tier          varchar(4)    NOT NULL DEFAULT 'HOT' CHECK (tier IN ('HOT', 'WARM', 'COLD')),
    state         varchar(7)    NOT NULL DEFAULT 'OPEN' CHECK (state IN ('OPEN', 'SEALED', 'DELETED')),
    location      text          NOT NULL,
    doc_count     bigint,
    fingerprint   numeric(20,0),
    size_bytes    bigint,
    last_write_at timestamptz,
    sealed_at     timestamptz,
    version       int           NOT NULL DEFAULT 1,
    created_at    timestamptz   NOT NULL DEFAULT now(),
    updated_at    timestamptz   NOT NULL DEFAULT now(),
    CHECK (bucket_end > bucket_start)
);

CREATE TABLE migration_jobs (
    job_id          bigserial     PRIMARY KEY,
    partition_id    bigint        NOT NULL REFERENCES data_partitions,
    job_type        varchar(12)   NOT NULL CHECK (job_type IN ('HOT_TO_WARM', 'WARM_TO_COLD', 'DELETE')),
    status          varchar(14)   NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                        'PENDING', 'COPYING', 'VERIFYING', 'SWITCHING',
                        'PURGE_PENDING', 'ROLLING_BACK', 'DONE', 'FAILED')),
    attempt         smallint      NOT NULL DEFAULT 0,
    max_attempts    smallint      NOT NULL DEFAULT 3,
    staging_uri     text,
    src_count       bigint,
    dst_count       bigint,
    src_fingerprint numeric(20,0),
    dst_fingerprint numeric(20,0),
    purge_after     timestamptz,
    next_run_at     timestamptz   NOT NULL DEFAULT now(),
    error_msg       text,
    created_at      timestamptz   NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);

-- At most one unfinished job per partition (INV-6).
CREATE UNIQUE INDEX migration_jobs_one_active ON migration_jobs (partition_id)
    WHERE status NOT IN ('DONE', 'FAILED');

CREATE TABLE ingest_batches (
    batch_id       bigserial   PRIMARY KEY,
    source_id      smallint    NOT NULL REFERENCES log_sources,
    file_name      text        NOT NULL,
    file_sha256    char(64)    NOT NULL,
    bronze_uri     text,
    rows_read      bigint      NOT NULL DEFAULT 0,
    rows_loaded    bigint      NOT NULL DEFAULT 0,
    rows_rejected  bigint      NOT NULL DEFAULT 0,
    rows_duplicate bigint      NOT NULL DEFAULT 0,
    status         varchar(8)  NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING', 'SUCCESS', 'PARTIAL', 'FAILED')),
    started_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);

CREATE INDEX ingest_batches_file_sha256 ON ingest_batches (file_sha256);

CREATE TABLE rejected_records (
    reject_id     bigserial   PRIMARY KEY,
    batch_id      bigint      NOT NULL REFERENCES ingest_batches,
    record_offset bigint      NOT NULL,
    reason        varchar(40) NOT NULL,
    detail        text,
    raw_record    text,
    rejected_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE query_audit (
    query_id           uuid         PRIMARY KEY,
    requested_at       timestamptz  NOT NULL DEFAULT now(),
    query_text         varchar(200),
    time_from          timestamptz  NOT NULL,
    time_to            timestamptz  NOT NULL,
    tiers              text[]       NOT NULL,
    partitions_planned int          NOT NULL,
    partitions_failed  int          NOT NULL DEFAULT 0,
    result_count       int          NOT NULL,
    duration_ms        int          NOT NULL,
    tier_durations     jsonb,
    status             varchar(8)   NOT NULL CHECK (status IN ('COMPLETE', 'PARTIAL', 'FAILED'))
);
