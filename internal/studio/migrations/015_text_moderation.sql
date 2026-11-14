-- Frozen policy and safe decisions only: never candidate text, endpoint or keys.
CREATE TABLE studio_run_moderation (
 run_id text PRIMARY KEY REFERENCES studio_runs(id),
 policy_id text NOT NULL,
 policy_version text NOT NULL,
 policy_digest text NOT NULL CHECK(policy_digest ~ '^[a-f0-9]{64}$'),
 policy jsonb NOT NULL,
 state text NOT NULL CHECK(state IN('PENDING','ALLOWED','BLOCKED','REVIEW')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 reason_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((state='PENDING' AND reason_code='') OR (state='ALLOWED' AND reason_code='allowed') OR (state='BLOCKED' AND reason_code='policy_block') OR (state='REVIEW' AND reason_code IN('policy_review','check_unavailable','check_protocol','check_timeout','check_busy','invalid_content','content_too_large','policy_unavailable','generation_incomplete','execution_incomplete','process_restarted','cancelled')))
);
CREATE TABLE studio_moderation_decisions (
 run_id text NOT NULL REFERENCES studio_run_moderation(run_id),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 state text NOT NULL CHECK(state IN('PENDING','ALLOWED','BLOCKED','REVIEW')),
 reason_code text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(run_id,version)
);
CREATE FUNCTION studio_guard_moderation_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'moderation evidence cannot be deleted'; END IF;
 IF (NEW.run_id,NEW.policy_id,NEW.policy_version,NEW.policy_digest,NEW.policy,NEW.created_at) IS DISTINCT FROM (OLD.run_id,OLD.policy_id,OLD.policy_version,OLD.policy_digest,OLD.policy,OLD.created_at) THEN RAISE EXCEPTION 'moderation policy is immutable'; END IF;
 IF OLD.state<>'PENDING' OR NEW.state='PENDING' OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'moderation decision transition invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_moderation_policy_guard BEFORE UPDATE OR DELETE ON studio_run_moderation FOR EACH ROW EXECUTE FUNCTION studio_guard_moderation_policy();
CREATE TRIGGER studio_moderation_decision_immutable BEFORE UPDATE OR DELETE ON studio_moderation_decisions FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
