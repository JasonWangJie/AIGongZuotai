-- Existing 023 definitions, selected images, child links and receipts stay
-- untouched. Only a newly started four-node graph receives a video intent.
ALTER TABLE studio_workflow_executions
 DROP CONSTRAINT studio_workflow_executions_status_check,
 DROP CONSTRAINT studio_workflow_executions_check,
 ADD COLUMN video_task_id text,
 ADD COLUMN video_artifacts jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(video_artifacts)='array' AND jsonb_array_length(video_artifacts)<=1),
 ADD CONSTRAINT studio_workflow_video_task_scope FOREIGN KEY(video_task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 ADD CONSTRAINT studio_workflow_execution_status CHECK(status IN ('TEXT_READY','TEXT_ACTIVE','IMAGE_READY','IMAGE_ACTIVE','WAITING_APPROVAL','VIDEO_READY','VIDEO_ACTIVE','COMPLETED','FAILED','REVIEW')),
 ADD CONSTRAINT studio_workflow_selected_stage CHECK(
   CASE WHEN status IN ('VIDEO_READY','VIDEO_ACTIVE','COMPLETED') THEN selected IS NOT NULL
        WHEN status IN ('FAILED','REVIEW') THEN selected IS NULL OR video_task_id IS NOT NULL
        ELSE selected IS NULL END),
 ADD CONSTRAINT studio_workflow_video_link CHECK(video_task_id IS NULL OR selected IS NOT NULL);
CREATE UNIQUE INDEX studio_workflow_video_child ON studio_workflow_executions(video_task_id) WHERE video_task_id IS NOT NULL;
ALTER TABLE studio_workflow_intents DROP CONSTRAINT studio_workflow_intents_kind_check,
 ADD CONSTRAINT studio_workflow_intent_kind CHECK(kind IN ('text','image','video'));
