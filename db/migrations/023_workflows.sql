CREATE TABLE studio_workflow_definitions (
 id text PRIMARY KEY, workspace_id text NOT NULL, project_id text NOT NULL,
 version integer NOT NULL CHECK(version BETWEEN 1 AND 64),
 UNIQUE(id,project_id,workspace_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE TABLE studio_workflow_versions (
 definition_id text NOT NULL, version integer NOT NULL CHECK(version BETWEEN 1 AND 64),
 workspace_id text NOT NULL, project_id text NOT NULL,
 graph jsonb NOT NULL CHECK(octet_length(graph::text)<=32768),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(definition_id,version), UNIQUE(definition_id,version,project_id,workspace_id),
 FOREIGN KEY(definition_id,project_id,workspace_id) REFERENCES studio_workflow_definitions(id,project_id,workspace_id)
);
CREATE TRIGGER studio_workflow_version_immutable BEFORE UPDATE OR DELETE ON studio_workflow_versions
 FOR EACH ROW EXECUTE FUNCTION studio_reject_library_revision_mutation();
CREATE INDEX studio_workflow_definition_page ON studio_workflow_definitions(workspace_id,project_id,id);

CREATE TABLE studio_workflow_executions (
 id text PRIMARY KEY, workspace_id text NOT NULL, project_id text NOT NULL,
 definition_id text NOT NULL, definition_version integer NOT NULL,
 input text NOT NULL CHECK(octet_length(input) BETWEEN 1 AND 16000),
 models jsonb NOT NULL CHECK(octet_length(models::text)<=131072),
 provider_mode text NOT NULL CHECK(provider_mode IN ('demo','live')),
 version integer NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 16),
 status text NOT NULL CHECK(status IN ('TEXT_READY','TEXT_ACTIVE','IMAGE_READY','IMAGE_ACTIVE','WAITING_APPROVAL','COMPLETED','FAILED','REVIEW')),
 text_run_id text, image_task_id text,
 candidates jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(candidates)='array' AND jsonb_array_length(candidates)<=16),
 selected jsonb, error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(definition_id,definition_version,project_id,workspace_id) REFERENCES studio_workflow_versions(definition_id,version,project_id,workspace_id),
 FOREIGN KEY(text_run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id),
 FOREIGN KEY(image_task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 CHECK((status='COMPLETED')=(selected IS NOT NULL))
);
CREATE INDEX studio_workflow_execution_page ON studio_workflow_executions(workspace_id,project_id,created_at DESC,id);
CREATE UNIQUE INDEX studio_workflow_text_child ON studio_workflow_executions(text_run_id) WHERE text_run_id IS NOT NULL;
CREATE UNIQUE INDEX studio_workflow_image_child ON studio_workflow_executions(image_task_id) WHERE image_task_id IS NOT NULL;
CREATE TABLE studio_workflow_intents (
 execution_id text NOT NULL REFERENCES studio_workflow_executions(id),
 kind text NOT NULL CHECK(kind IN ('text','image')), child_key text UNIQUE NOT NULL,
 action_key text NOT NULL DEFAULT '', body jsonb CHECK(octet_length(body::text)<=131072),
 PRIMARY KEY(execution_id,kind), CHECK((body IS NULL)=(action_key=''))
);
CREATE TABLE studio_workflow_receipts (
 workspace_id text NOT NULL, project_id text NOT NULL, operation text NOT NULL CHECK(operation IN ('save','start','confirm')),
 operation_key text NOT NULL CHECK(length(operation_key) BETWEEN 8 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32), resource_id text NOT NULL, version integer NOT NULL,
 PRIMARY KEY(workspace_id,project_id,operation,operation_key),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
