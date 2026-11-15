-- Private byte identities and frozen policies; never provider URLs or secrets.
CREATE TABLE studio_task_media_moderation (
 task_id text PRIMARY KEY,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 policy jsonb NOT NULL CHECK(jsonb_typeof(policy)='object'),
 text_policy jsonb NOT NULL CHECK(jsonb_typeof(text_policy)='object'),
 policy_id text NOT NULL,
 policy_version text NOT NULL,
 policy_digest text NOT NULL CHECK(policy_digest ~ '^[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN('PENDING','ALLOWED','BLOCKED','REVIEW')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 reason_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 CHECK((state='PENDING' AND version=1 AND reason_code='') OR (state='ALLOWED' AND version=2 AND reason_code='allowed') OR (state='BLOCKED' AND version=2 AND reason_code='policy_block') OR (state='REVIEW' AND version=2 AND reason_code IN('policy_review','check_unavailable','check_protocol','check_timeout','check_busy','invalid_content','content_too_large','policy_unavailable','generation_incomplete','execution_incomplete','process_restarted','cancelled')))
);
CREATE TABLE studio_task_media_candidates (
 task_id text PRIMARY KEY REFERENCES studio_task_media_moderation(task_id),
 candidates jsonb NOT NULL CHECK(jsonb_typeof(candidates)='array' AND jsonb_array_length(candidates) BETWEEN 1 AND 4),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE studio_media_moderation_decisions (
 task_id text NOT NULL REFERENCES studio_task_media_moderation(task_id),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 state text NOT NULL CHECK(state IN('PENDING','ALLOWED','BLOCKED','REVIEW')),
 reason_code text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(task_id,version)
);
CREATE TABLE studio_reference_media_moderation (
 reference_id text PRIMARY KEY,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 policy jsonb NOT NULL CHECK(jsonb_typeof(policy)='object'),
 policy_id text NOT NULL,
 policy_version text NOT NULL,
 policy_digest text NOT NULL CHECK(policy_digest ~ '^[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state='ALLOWED'),
 version bigint NOT NULL CHECK(version=2),
 reason_code text NOT NULL CHECK(reason_code='allowed'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(reference_id,project_id,workspace_id) REFERENCES studio_references(id,project_id,workspace_id)
);
CREATE FUNCTION studio_guard_media_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'media moderation evidence cannot be deleted'; END IF;
 IF (NEW.task_id,NEW.workspace_id,NEW.project_id,NEW.policy,NEW.text_policy,NEW.policy_id,NEW.policy_version,NEW.policy_digest,NEW.created_at) IS DISTINCT FROM (OLD.task_id,OLD.workspace_id,OLD.project_id,OLD.policy,OLD.text_policy,OLD.policy_id,OLD.policy_version,OLD.policy_digest,OLD.created_at) THEN RAISE EXCEPTION 'media policy is immutable'; END IF;
 IF OLD.state<>'PENDING' OR NEW.state='PENDING' OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'media decision transition invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_media_policy_guard BEFORE UPDATE OR DELETE ON studio_task_media_moderation FOR EACH ROW EXECUTE FUNCTION studio_guard_media_policy();
CREATE TRIGGER studio_media_candidates_immutable BEFORE UPDATE OR DELETE ON studio_task_media_candidates FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_media_decisions_immutable BEFORE UPDATE OR DELETE ON studio_media_moderation_decisions FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_reference_media_immutable BEFORE UPDATE OR DELETE ON studio_reference_media_moderation FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
