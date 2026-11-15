CREATE TABLE studio_audit_events (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 project_id text NOT NULL,
 actor_id text NOT NULL,
 actor_type text NOT NULL CHECK(actor_type IN ('user','local')),
 action text NOT NULL CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.reconcile_requested','draft.saved')),
 resource_type text NOT NULL CHECK(resource_type IN ('project','run','task','draft')),
 resource_id text NOT NULL,
 created_at timestamptz NOT NULL,
 details jsonb NOT NULL CHECK(jsonb_typeof(details)='object'),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_audit_project_page ON studio_audit_events(workspace_id,project_id,created_at DESC,id DESC);
CREATE INDEX studio_audit_workspace_page ON studio_audit_events(workspace_id,created_at DESC,id DESC);
