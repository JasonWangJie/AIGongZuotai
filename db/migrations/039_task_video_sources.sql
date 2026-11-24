-- Only new source-video operations have a confirmed source to register.
CREATE TABLE studio_task_video_sources (
 task_id text PRIMARY KEY,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 actor_id text NOT NULL REFERENCES studio_users(id),
 operation text NOT NULL CHECK (operation IN ('video_edit','video_extend')),
 asset_id text NOT NULL,
 asset_revision bigint NOT NULL CHECK (asset_revision > 0),
 source_task_id text NOT NULL CHECK (source_task_id <> task_id),
 artifact_id text NOT NULL,
 content_version text NOT NULL CHECK (content_version ~ '^[a-f0-9]{64}$'),
 FOREIGN KEY (task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 FOREIGN KEY (source_task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 FOREIGN KEY (asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id),
 FOREIGN KEY (asset_id,asset_revision) REFERENCES studio_library_revisions(asset_id,revision)
);
CREATE TRIGGER studio_task_video_source_immutable BEFORE UPDATE OR DELETE ON studio_task_video_sources
 FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
