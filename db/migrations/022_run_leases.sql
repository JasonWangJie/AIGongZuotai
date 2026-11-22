-- The process owning the control writer rotates this generation during open.
CREATE TABLE studio_run_executor (
    singleton boolean PRIMARY KEY CHECK (singleton),
    generation text NOT NULL
);
INSERT INTO studio_run_executor(singleton,generation) VALUES (true,'');

-- Existing 021 streams have no lease evidence. Recovery retains the original
-- execution, prefix and replay floor without granting a new execution.
ALTER TABLE studio_run_streams
    ADD COLUMN lease_required boolean NOT NULL DEFAULT false,
    ADD COLUMN lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN lease_generation text NOT NULL DEFAULT '',
    ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    ADD COLUMN lease_until timestamptz,
    ADD CONSTRAINT studio_run_lease_identity CHECK (
        (lease_owner='' AND lease_generation='' AND lease_until IS NULL) OR
        (lease_required AND execution_id<>'' AND lease_epoch>0 AND
         lease_owner<>'' AND lease_generation<>'' AND lease_until IS NOT NULL)
    );
ALTER TABLE studio_run_streams ALTER COLUMN lease_required SET DEFAULT true;
