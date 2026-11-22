-- A project row serializes first and later saves. Each immutable version is
-- also the creator's durable save receipt.
CREATE TABLE studio_canvas_versions (
 project_id text NOT NULL, workspace_id text NOT NULL, revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 64),
 created_by text NOT NULL REFERENCES studio_users(id), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32), request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 node_count integer NOT NULL CHECK(node_count BETWEEN 0 AND 2000), edge_count integer NOT NULL CHECK(edge_count BETWEEN 0 AND 4000),
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object' AND document ?& ARRAY['title','nodes','edges','groups'] AND
  jsonb_typeof(document->'nodes')='array' AND jsonb_array_length(document->'nodes')=node_count AND
  jsonb_typeof(document->'edges')='array' AND jsonb_array_length(document->'edges')=edge_count AND
  jsonb_typeof(document->'groups')='array' AND jsonb_array_length(document->'groups')<=128 AND document->>'title'=title AND
  octet_length(document::text)<=3145728),
 PRIMARY KEY(project_id,revision), UNIQUE(project_id,workspace_id,revision), UNIQUE(project_id,created_by,key_hash),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE TABLE studio_canvas_sources (
 project_id text NOT NULL, workspace_id text NOT NULL, revision bigint NOT NULL,
 node_id text NOT NULL CHECK(node_id~'^[A-Za-z0-9_-]{1,48}$'),
 asset_id text, asset_revision bigint, original jsonb, run_id text,
 PRIMARY KEY(project_id,revision,node_id),
 FOREIGN KEY(project_id,workspace_id,revision) REFERENCES studio_canvas_versions(project_id,workspace_id,revision),
 FOREIGN KEY(asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id),
 FOREIGN KEY(asset_id,asset_revision) REFERENCES studio_library_revisions(asset_id,revision),
 FOREIGN KEY(run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id),
 CHECK((run_id IS NOT NULL AND asset_id IS NULL AND asset_revision IS NULL AND original IS NULL) OR
       (run_id IS NULL AND asset_id IS NOT NULL AND asset_revision IS NOT NULL AND asset_revision>0 AND
        original IS NOT NULL AND jsonb_typeof(original)='object' AND octet_length(original::text)<=32768))
);
CREATE TRIGGER studio_canvas_version_immutable BEFORE UPDATE OR DELETE ON studio_canvas_versions FOR EACH ROW EXECUTE FUNCTION studio_guard_chat_immutable();
CREATE TRIGGER studio_canvas_source_immutable BEFORE UPDATE OR DELETE ON studio_canvas_sources FOR EACH ROW EXECUTE FUNCTION studio_guard_chat_immutable();
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_action_check,
 ADD CONSTRAINT studio_audit_events_action_check CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.submit_unknown','task.reconcile_requested','draft.saved','reference.uploaded','asset.updated','asset.trashed','asset.restored','conversation.created','conversation.turn_created','canvas.saved')),
 DROP CONSTRAINT studio_audit_events_resource_type_check,
 ADD CONSTRAINT studio_audit_events_resource_type_check CHECK(resource_type IN ('project','run','task','draft','reference','asset','conversation','canvas'));
ALTER TABLE studio_outbox_events DROP CONSTRAINT studio_outbox_events_resource_type_check,
 ADD CONSTRAINT studio_outbox_events_resource_type_check CHECK(resource_type IN ('task','run','project','draft','reference','asset','conversation','workflow','canvas'));
