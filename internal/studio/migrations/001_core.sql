CREATE TABLE IF NOT EXISTS studio_schema_versions(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS studio_users (
 id text PRIMARY KEY, email text UNIQUE NOT NULL, name text NOT NULL,
 password_hash bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS studio_workspaces (
 id text PRIMARY KEY, owner_id text REFERENCES studio_users(id), name text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS studio_projects (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES studio_workspaces(id), name text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(id,workspace_id)
);
CREATE INDEX IF NOT EXISTS studio_projects_workspace ON studio_projects(workspace_id,created_at,id);
CREATE TABLE IF NOT EXISTS studio_sessions (
 token_hash bytea PRIMARY KEY, user_id text NOT NULL REFERENCES studio_users(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id), expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS studio_sessions_expiry ON studio_sessions(expires_at);
CREATE TABLE IF NOT EXISTS studio_runs (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES studio_workspaces(id), project_id text NOT NULL,
 document jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX IF NOT EXISTS studio_runs_project ON studio_runs(workspace_id,project_id,updated_at DESC);
CREATE TABLE IF NOT EXISTS studio_tasks (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES studio_workspaces(id), project_id text NOT NULL,
 document jsonb NOT NULL, input jsonb NOT NULL, handle jsonb NOT NULL, submission jsonb, next_poll timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX IF NOT EXISTS studio_tasks_project ON studio_tasks(workspace_id,project_id,updated_at DESC);
CREATE TABLE IF NOT EXISTS studio_idempotency (
 scope_key text PRIMARY KEY, payload_hash bytea NOT NULL, resource_id text NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN ('run','task')), created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO studio_schema_versions(version) VALUES(1) ON CONFLICT DO NOTHING;
