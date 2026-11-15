CREATE TABLE studio_operations_cases (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 project_id text NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN('run','task')),
 resource_id text NOT NULL,
 resource_version bigint NOT NULL CHECK(resource_version>=0),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN('OPEN','RESOLVED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 reason text NOT NULL,
 evidence_ref text NOT NULL,
 created_by text NOT NULL REFERENCES studio_users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE UNIQUE INDEX studio_operations_case_open_resource ON studio_operations_cases(workspace_id,resource_type,resource_id) WHERE state='OPEN';
CREATE INDEX studio_operations_cases_page ON studio_operations_cases(workspace_id,created_at DESC,id DESC);
CREATE TABLE studio_operations_case_idempotency (
 actor_id text NOT NULL REFERENCES studio_users(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 idempotency_key text NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 case_id text NOT NULL REFERENCES studio_operations_cases(id),
 PRIMARY KEY(actor_id,workspace_id,idempotency_key)
);
CREATE TABLE studio_operations_plans (
 id text PRIMARY KEY,
 case_id text NOT NULL REFERENCES studio_operations_cases(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 actor_id text NOT NULL REFERENCES studio_users(id),
 snapshot jsonb NOT NULL,
 digest text NOT NULL CHECK(length(digest)=64),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 consumed_at timestamptz
);
CREATE INDEX studio_operations_plans_active ON studio_operations_plans(workspace_id,expires_at) WHERE consumed_at IS NULL;
CREATE INDEX studio_operations_plans_cleanup ON studio_operations_plans(expires_at,id) WHERE consumed_at IS NULL;
CREATE TABLE studio_operations_receipts (
 id text PRIMARY KEY,
 case_id text NOT NULL UNIQUE REFERENCES studio_operations_cases(id),
 plan_id text NOT NULL UNIQUE REFERENCES studio_operations_plans(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 document jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER studio_operations_receipt_immutable BEFORE UPDATE OR DELETE ON studio_operations_receipts FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE INDEX studio_operations_run_exceptions ON studio_runs(workspace_id,updated_at DESC,id DESC) WHERE document->>'status' IN('FAILED','INTERRUPTED') OR document->'billing'->>'state'='REVIEW';
CREATE INDEX studio_operations_task_exceptions ON studio_tasks(workspace_id,updated_at DESC,id DESC) WHERE document->>'status' IN('FAILED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED','PROVIDER_STATE_UNKNOWN') OR document->'billing'->>'state'='REVIEW';
