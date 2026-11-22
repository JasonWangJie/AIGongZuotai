-- A fork owns only its new children. Inherited, read-only links are outside
-- the unique indexes used to dispatch outcomes to the original parent.
ALTER TABLE studio_workflow_executions
 ADD CONSTRAINT studio_workflow_scoped_id UNIQUE(id,project_id,workspace_id),
 ADD COLUMN source_workflow_id text,
 ADD COLUMN source_version integer CHECK(source_version BETWEEN 1 AND 16),
 ADD COLUMN from_node text,
 ADD COLUMN attempt integer NOT NULL DEFAULT 1 CHECK(attempt BETWEEN 1 AND 128),
 ADD COLUMN graph_override jsonb CHECK(octet_length(graph_override::text)<=32768),
 ADD COLUMN inherited_text_run_id text,
 ADD COLUMN inherited_image_task_id text,
 ADD CONSTRAINT studio_workflow_source FOREIGN KEY(source_workflow_id,project_id,workspace_id) REFERENCES studio_workflow_executions(id,project_id,workspace_id),
 ADD CONSTRAINT studio_workflow_inherited_text FOREIGN KEY(inherited_text_run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id),
 ADD CONSTRAINT studio_workflow_inherited_image FOREIGN KEY(inherited_image_task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 ADD CONSTRAINT studio_workflow_attempt_origin CHECK(
   (attempt=1 AND source_workflow_id IS NULL AND source_version IS NULL AND from_node IS NULL AND graph_override IS NULL AND inherited_text_run_id IS NULL AND inherited_image_task_id IS NULL)
   OR (attempt>1 AND source_workflow_id IS NOT NULL AND source_workflow_id<>id AND source_version IS NOT NULL AND from_node IS NOT NULL AND length(from_node) BETWEEN 1 AND 40)),
 ADD CONSTRAINT studio_workflow_attempt_ownership CHECK(
   (inherited_text_run_id IS NULL OR text_run_id IS NULL) AND
   (inherited_image_task_id IS NULL OR (image_task_id IS NULL AND inherited_text_run_id IS NOT NULL))),
 DROP CONSTRAINT studio_workflow_execution_status,
 DROP CONSTRAINT studio_workflow_selected_stage,
 ADD CONSTRAINT studio_workflow_execution_status CHECK(status IN ('TEXT_READY','TEXT_ACTIVE','IMAGE_READY','IMAGE_ACTIVE','WAITING_APPROVAL','VIDEO_READY','VIDEO_ACTIVE','COMPLETED','FAILED','REVIEW','CANCELLED')),
 ADD CONSTRAINT studio_workflow_selected_stage CHECK(
   CASE WHEN status IN ('VIDEO_READY','VIDEO_ACTIVE','COMPLETED') THEN selected IS NOT NULL
        WHEN status='CANCELLED' THEN true
        WHEN status IN ('FAILED','REVIEW') THEN selected IS NULL OR video_task_id IS NOT NULL
        ELSE selected IS NULL END);
ALTER TABLE studio_workflow_receipts DROP CONSTRAINT studio_workflow_receipts_operation_check,
 ADD CONSTRAINT studio_workflow_receipts_operation_check CHECK(operation IN ('save','start','confirm','rerun','cancel'));
CREATE INDEX studio_workflow_queued_task_cancel ON studio_audit_events(workspace_id,project_id,resource_id)
 WHERE action='task.cancelled' AND details->>'from_status'='QUEUED';
ALTER TABLE studio_outbox_events
 DROP CONSTRAINT studio_outbox_events_kind_check,
 DROP CONSTRAINT studio_outbox_events_resource_type_check,
 ADD CONSTRAINT studio_outbox_events_kind_check CHECK(kind IN ('task.updated','run.updated','audit.created','workflow.updated')),
 ADD CONSTRAINT studio_outbox_events_resource_type_check CHECK(resource_type IN ('task','run','project','draft','reference','asset','conversation','workflow'));
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_action_check,
 ADD CONSTRAINT studio_audit_events_action_check CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.submit_unknown','task.reconcile_requested','draft.saved','reference.uploaded','asset.updated','asset.trashed','asset.restored','conversation.created','conversation.turn_created'));
