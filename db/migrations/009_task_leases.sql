-- Lease metadata is private scheduling state; task document/version and the
-- exact accepted submission remain the public/business source of truth.
ALTER TABLE studio_tasks ADD COLUMN lease_owner text NOT NULL DEFAULT '';
ALTER TABLE studio_tasks ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0 CHECK(lease_epoch>=0);
ALTER TABLE studio_tasks ADD COLUMN lease_until timestamptz;
ALTER TABLE studio_tasks ADD CONSTRAINT studio_task_lease_shape CHECK((lease_owner='' AND lease_until IS NULL) OR (lease_owner<>'' AND lease_until IS NOT NULL));
CREATE TABLE studio_task_executor (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 generation text NOT NULL
);
INSERT INTO studio_task_executor(singleton,generation) VALUES(true,'');
CREATE INDEX studio_tasks_due_leases ON studio_tasks(next_poll,id) WHERE document->>'status' NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED');
