-- Only accepted tasks get a row. The task, reservation, idempotency record,
-- outbox and immutable panel binding commit together.
CREATE TABLE studio_comic_panel_videos (
 task_id text PRIMARY KEY,
 workspace_id text NOT NULL, project_id text NOT NULL, comic_version bigint NOT NULL,
 panel_id text NOT NULL CHECK(panel_id ~ '^[A-Za-z0-9_-]{1,48}$'),
 attempt integer NOT NULL CHECK(attempt BETWEEN 1 AND 32),
 actor_id text NOT NULL REFERENCES studio_users(id),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 quote_id text REFERENCES studio_ledger_quotes(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,comic_version,panel_id,attempt),
 UNIQUE(project_id,actor_id,key_hash),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 FOREIGN KEY(project_id,workspace_id,comic_version) REFERENCES studio_comic_versions(project_id,workspace_id,version)
);
CREATE TRIGGER studio_comic_panel_video_immutable BEFORE UPDATE OR DELETE ON studio_comic_panel_videos
 FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
-- Repeated previews for one operation can read its still-valid, unspent quote.
CREATE INDEX studio_panel_quote_reuse ON studio_ledger_quotes(workspace_id,project_id,owner_user_id,request_hash,created_at DESC,id)
 WHERE reservation_id IS NULL;
