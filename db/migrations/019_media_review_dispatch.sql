-- New first-review evidence only. Existing candidates are never backfilled.
CREATE TABLE studio_media_review_dispatch (
 task_id text PRIMARY KEY REFERENCES studio_task_media_candidates(task_id),
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 candidate_digest text NOT NULL CHECK(candidate_digest ~ '^[a-f0-9]{64}$'),
 policy_digest text NOT NULL CHECK(policy_digest ~ '^[a-f0-9]{64}$'),
 result_status text NOT NULL CHECK(result_status IN('SUCCEEDED','PARTIAL')),
 state text NOT NULL CHECK(state IN('READY','STARTED','FINISHED')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 3),
 attempt_id text NOT NULL DEFAULT '' CHECK(attempt_id='' OR attempt_id ~ '^review_[a-f0-9]{32}$'),
 started_at timestamptz,
 finished_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 CHECK((attempt_id='')=(started_at IS NULL)),
 CHECK((state='READY' AND version=1 AND attempt_id='' AND finished_at IS NULL)
 OR (state='STARTED' AND version=2 AND attempt_id<>'' AND finished_at IS NULL)
 OR (state='FINISHED' AND finished_at IS NOT NULL AND ((version=2 AND attempt_id='') OR (version=3 AND attempt_id<>''))))
);
CREATE INDEX studio_media_review_dispatch_pending ON studio_media_review_dispatch(task_id) WHERE state IN('READY','STARTED');
CREATE FUNCTION studio_guard_media_review_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'media review dispatch evidence cannot be deleted'; END IF;
 IF (NEW.task_id,NEW.workspace_id,NEW.project_id,NEW.candidate_digest,NEW.policy_digest,NEW.result_status,NEW.created_at) IS DISTINCT FROM (OLD.task_id,OLD.workspace_id,OLD.project_id,OLD.candidate_digest,OLD.policy_digest,OLD.result_status,OLD.created_at) THEN RAISE EXCEPTION 'media review dispatch identity is immutable'; END IF;
 IF OLD.state='FINISHED' OR NEW.version<>OLD.version+1 OR NOT ((OLD.state='READY' AND NEW.state IN('STARTED','FINISHED')) OR (OLD.state='STARTED' AND NEW.state='FINISHED')) THEN RAISE EXCEPTION 'media review dispatch transition invalid'; END IF;
 IF OLD.state='STARTED' AND (NEW.attempt_id,NEW.started_at) IS DISTINCT FROM (OLD.attempt_id,OLD.started_at) THEN RAISE EXCEPTION 'media review attempt is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_media_review_dispatch_guard BEFORE UPDATE OR DELETE ON studio_media_review_dispatch FOR EACH ROW EXECUTE FUNCTION studio_guard_media_review_dispatch();
