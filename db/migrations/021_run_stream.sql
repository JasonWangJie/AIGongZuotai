-- Existing runs keep their 020 checkpoint / volatile-cursor recovery rules.
CREATE TABLE studio_run_streams (
    run_id text PRIMARY KEY REFERENCES studio_runs(id),
    execution_id text NOT NULL DEFAULT '',
    head_seq bigint NOT NULL CHECK (head_seq BETWEEN 0 AND 9007199254740991),
    floor_seq bigint NOT NULL CHECK (floor_seq BETWEEN 0 AND head_seq),
    snapshot_seq bigint NOT NULL CHECK (snapshot_seq BETWEEN floor_seq AND head_seq),
    retained_bytes integer NOT NULL DEFAULT 0 CHECK (retained_bytes BETWEEN 0 AND 1048576),
    output_bytes integer NOT NULL CHECK (output_bytes BETWEEN 0 AND 131072),
    closed boolean NOT NULL,
    moderated boolean NOT NULL
);

CREATE TABLE studio_run_stream_events (
    run_id text NOT NULL REFERENCES studio_run_streams(run_id),
    seq bigint NOT NULL CHECK (seq BETWEEN 1 AND 9007199254740991),
    execution_id text NOT NULL,
    name text NOT NULL CHECK (name IN ('message.delta','run.snapshot','run.completed','run.cancelled','run.failed','run.interrupted')),
    delta text NOT NULL DEFAULT '',
    data jsonb,
    byte_size integer NOT NULL CHECK (byte_size BETWEEN 1 AND 1048576),
    emitted_at timestamptz NOT NULL,
    PRIMARY KEY (run_id,seq),
    CHECK ((name='message.delta' AND length(delta)>0 AND execution_id<>'' AND data IS NULL AND byte_size=octet_length(delta))
        OR (name<>'message.delta' AND delta='' AND data IS NOT NULL))
);
