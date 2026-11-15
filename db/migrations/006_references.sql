CREATE TABLE studio_references (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 project_id text NOT NULL,
 name text NOT NULL,
 blob jsonb NOT NULL CHECK(jsonb_typeof(blob)='object'),
 stored_bytes bigint NOT NULL CHECK(stored_bytes>0),
 created_at timestamptz NOT NULL,
 UNIQUE(id,project_id,workspace_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_references_project_page ON studio_references(workspace_id,project_id,created_at DESC,id DESC);
CREATE TABLE studio_reference_idempotency (
 scope_key text PRIMARY KEY,
 payload_hash bytea NOT NULL,
 reference_id text NOT NULL REFERENCES studio_references(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE studio_tasks ADD CONSTRAINT studio_tasks_reference_identity UNIQUE(id,project_id,workspace_id);
CREATE TABLE studio_task_references (
 task_id text NOT NULL,
 position smallint NOT NULL CHECK(position BETWEEN 0 AND 3),
 reference_id text NOT NULL,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 PRIMARY KEY(task_id,position),
 UNIQUE(task_id,reference_id),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 FOREIGN KEY(reference_id,project_id,workspace_id) REFERENCES studio_references(id,project_id,workspace_id)
);
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_action_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_action_check CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.reconcile_requested','draft.saved','reference.uploaded'));
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_resource_type_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_resource_type_check CHECK(resource_type IN ('project','run','task','draft','reference'));
