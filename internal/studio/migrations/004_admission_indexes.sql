-- Keep this exact terminal predicate aligned with terminal() and admissionCounts.
-- Historical terminal rows are excluded; unresolved provider work stays indexed.
CREATE INDEX studio_runs_admission_active ON studio_runs(workspace_id,(document->>'model_id'))
 WHERE document->>'status' NOT IN ('SUCCEEDED','COMPLETED','FAILED','CANCELLED','INTERRUPTED','PARTIAL');
CREATE INDEX studio_tasks_admission_active ON studio_tasks(workspace_id,(document->>'model_id'))
 WHERE document->>'status' NOT IN ('SUCCEEDED','COMPLETED','FAILED','CANCELLED','INTERRUPTED','PARTIAL');
