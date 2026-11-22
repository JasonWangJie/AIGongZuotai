-- An offline grant addresses one operator and one target account.
CREATE TABLE studio_account_governance_grants (
 id text PRIMARY KEY CHECK(id ~ '^agnt_[a-f0-9]{32}$'),
 operator_user_id text NOT NULL REFERENCES studio_users(id),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991 AND (NOT active OR version<9007199254740991)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(operator_user_id,target_user_id),
 CHECK(operator_user_id<>target_user_id)
);
CREATE INDEX studio_account_grants_active_target ON studio_account_governance_grants(target_user_id) WHERE active;
CREATE INDEX studio_account_grants_active_operator ON studio_account_governance_grants(operator_user_id,target_user_id) WHERE active;
CREATE FUNCTION studio_account_grant_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'target grant history cannot be deleted'; END IF;
 IF (NEW.id,NEW.operator_user_id,NEW.target_user_id,NEW.created_at) IS DISTINCT FROM
    (OLD.id,OLD.operator_user_id,OLD.target_user_id,OLD.created_at)
    OR NEW.active=OLD.active OR NEW.version<>OLD.version+1 THEN
  RAISE EXCEPTION 'target grant transition invalid';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_account_grant_guard BEFORE UPDATE OR DELETE ON studio_account_governance_grants FOR EACH ROW EXECUTE FUNCTION studio_account_grant_guard();
CREATE TABLE studio_account_grant_operations (
 operation_key text PRIMARY KEY CHECK(octet_length(operation_key) BETWEEN 1 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 operation_id text NOT NULL UNIQUE,
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE studio_account_governance_audit (
 id text PRIMARY KEY,
 actor_user_id text REFERENCES studio_users(id),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 action text NOT NULL CHECK(action IN('grant.created','grant.granted','grant.revoked','appeal.created','plan.previewed','account.suspended','account.lifted','appeal.upheld')),
 resource_id text NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 0 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((actor_user_id IS NULL)=(action IN('grant.created','grant.granted','grant.revoked')))
);
CREATE INDEX studio_account_governance_audit_target ON studio_account_governance_audit(target_user_id,created_at DESC,id DESC);
CREATE TRIGGER studio_account_grant_operations_immutable BEFORE UPDATE OR DELETE ON studio_account_grant_operations FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_account_audit_immutable BEFORE UPDATE OR DELETE ON studio_account_governance_audit FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();

CREATE TABLE studio_account_restrictions (
 id text PRIMARY KEY CHECK(id ~ '^arst_[a-f0-9]{32}$'),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 reason_code text NOT NULL CHECK(reason_code IN('manual_review','policy_concern','abuse_concern')),
 public_notice text NOT NULL CHECK(octet_length(public_notice) BETWEEN 1 AND 1024),
 evidence_ref text NOT NULL CHECK(octet_length(evidence_ref) BETWEEN 1 AND 128),
 created_by text NOT NULL REFERENCES studio_users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 released_at timestamptz,
 UNIQUE(id,target_user_id),
 CHECK(released_at IS NULL OR released_at>=created_at)
);
CREATE UNIQUE INDEX studio_account_restriction_active ON studio_account_restrictions(target_user_id) WHERE released_at IS NULL;
CREATE INDEX studio_account_restrictions_page ON studio_account_restrictions(target_user_id,created_at DESC,id DESC);
CREATE TABLE studio_account_controls (
 user_id text PRIMARY KEY REFERENCES studio_users(id),
 state text NOT NULL CHECK(state IN('OPEN','SUSPENDED')),
 version bigint NOT NULL CHECK(version BETWEEN 2 AND 9007199254740991),
 active_restriction_id text,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(active_restriction_id,user_id) REFERENCES studio_account_restrictions(id,target_user_id),
 CHECK((state='OPEN' AND active_restriction_id IS NULL) OR (state='SUSPENDED' AND active_restriction_id IS NOT NULL AND version<9007199254740991))
);
CREATE TABLE studio_account_appeals (
 id text PRIMARY KEY CHECK(id ~ '^aapl_[a-f0-9]{32}$'),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 restriction_id text NOT NULL,
 applicant_id text NOT NULL REFERENCES studio_users(id),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN('OPEN','RESOLVED')),
 outcome text NOT NULL DEFAULT '' CHECK(outcome IN('','UPHELD','LIFTED')),
 version bigint NOT NULL DEFAULT 1 CHECK(version IN(1,2)),
 reason_code text NOT NULL CHECK(reason_code IN('request_review','restriction_error','issue_resolved')),
 statement text NOT NULL DEFAULT '' CHECK(octet_length(statement)<=1024),
 public_reply text NOT NULL DEFAULT '' CHECK(octet_length(public_reply)<=1024),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,target_user_id,applicant_id),
 UNIQUE(restriction_id,applicant_id),
 CHECK(target_user_id=applicant_id),
 FOREIGN KEY(restriction_id,target_user_id) REFERENCES studio_account_restrictions(id,target_user_id),
 CHECK((state='OPEN' AND outcome='' AND version=1 AND public_reply='') OR (state='RESOLVED' AND outcome IN('UPHELD','LIFTED') AND version=2 AND octet_length(public_reply)>0))
);
CREATE INDEX studio_account_appeals_user_page ON studio_account_appeals(target_user_id,applicant_id,created_at DESC,id DESC);
CREATE INDEX studio_account_appeals_scope_page ON studio_account_appeals(target_user_id,created_at DESC,id DESC);
CREATE INDEX studio_account_appeals_open ON studio_account_appeals(target_user_id,restriction_id,id) WHERE state='OPEN';
CREATE TABLE studio_account_appeal_idempotency (
 target_user_id text NOT NULL REFERENCES studio_users(id),
 applicant_id text NOT NULL REFERENCES studio_users(id),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 appeal_id text NOT NULL,
 PRIMARY KEY(target_user_id,applicant_id,key_hash),
 FOREIGN KEY(appeal_id,target_user_id,applicant_id) REFERENCES studio_account_appeals(id,target_user_id,applicant_id)
);
CREATE TABLE studio_account_plans (
 id text PRIMARY KEY CHECK(id ~ '^aplan_[a-f0-9]{32}$'),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 actor_id text NOT NULL REFERENCES studio_users(id),
 action text NOT NULL CHECK(action IN('suspend','lift','uphold')),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 consumed_at timestamptz,
 UNIQUE(id,target_user_id)
);
CREATE INDEX studio_account_plans_active ON studio_account_plans(target_user_id,expires_at) WHERE consumed_at IS NULL;
CREATE INDEX studio_account_plans_cleanup ON studio_account_plans(expires_at,id) WHERE consumed_at IS NULL;
CREATE UNIQUE INDEX studio_account_lift_plan_pending ON studio_account_plans(target_user_id) WHERE action='lift' AND consumed_at IS NULL;
CREATE TABLE studio_account_receipts (
 id text PRIMARY KEY CHECK(id ~ '^arec_[a-f0-9]{32}$'),
 target_user_id text NOT NULL REFERENCES studio_users(id),
 plan_id text NOT NULL UNIQUE,
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(plan_id,target_user_id) REFERENCES studio_account_plans(id,target_user_id)
);
CREATE INDEX studio_account_receipts_scope ON studio_account_receipts(target_user_id,created_at DESC,id DESC);

CREATE FUNCTION studio_account_control_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'account control history cannot be deleted'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.version<>2 OR NEW.state<>'SUSPENDED' THEN RAISE EXCEPTION 'first account control must restrict at version 2'; END IF;
 ELSE
  IF NEW.user_id<>OLD.user_id OR NEW.version<>OLD.version+1 OR NEW.state=OLD.state THEN RAISE EXCEPTION 'account control transition invalid'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_account_control_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_account_controls FOR EACH ROW EXECUTE FUNCTION studio_account_control_guard();
CREATE FUNCTION studio_account_restriction_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'account restriction history cannot be deleted'; END IF;
 IF (NEW.id,NEW.target_user_id,NEW.reason_code,NEW.public_notice,NEW.evidence_ref,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.target_user_id,OLD.reason_code,OLD.public_notice,OLD.evidence_ref,OLD.created_by,OLD.created_at) OR OLD.released_at IS NOT NULL OR NEW.released_at IS NULL THEN RAISE EXCEPTION 'restriction origin immutable; release is one way'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_account_restriction_guard BEFORE UPDATE OR DELETE ON studio_account_restrictions FOR EACH ROW EXECUTE FUNCTION studio_account_restriction_guard();
-- Deferred: create or release updates both sides in one transaction/savepoint.
CREATE FUNCTION studio_account_active_consistency() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE ws text; BEGIN
 IF TG_TABLE_NAME='studio_account_controls' THEN ws:=NEW.user_id; ELSE ws:=NEW.target_user_id; END IF;
 IF EXISTS(SELECT 1 FROM studio_account_restrictions r LEFT JOIN studio_account_controls c ON c.user_id=r.target_user_id WHERE r.target_user_id=ws AND r.released_at IS NULL AND (c.state IS DISTINCT FROM 'SUSPENDED' OR c.active_restriction_id IS DISTINCT FROM r.id)) OR EXISTS(SELECT 1 FROM studio_account_controls c LEFT JOIN studio_account_restrictions r ON r.id=c.active_restriction_id AND r.target_user_id=c.user_id WHERE c.user_id=ws AND c.state='SUSPENDED' AND (r.id IS NULL OR r.released_at IS NOT NULL)) THEN RAISE EXCEPTION 'account control and active restriction disagree'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER studio_account_control_consistency AFTER INSERT OR UPDATE ON studio_account_controls DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_account_active_consistency();
CREATE CONSTRAINT TRIGGER studio_account_restriction_consistency AFTER INSERT OR UPDATE ON studio_account_restrictions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_account_active_consistency();
CREATE FUNCTION studio_account_appeal_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'account appeal history cannot be deleted'; END IF;
 IF (NEW.id,NEW.target_user_id,NEW.restriction_id,NEW.applicant_id,NEW.reason_code,NEW.statement,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.target_user_id,OLD.restriction_id,OLD.applicant_id,OLD.reason_code,OLD.statement,OLD.created_at) OR OLD.state<>'OPEN' OR NEW.state<>'RESOLVED' OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'account appeal transition invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_account_appeal_guard BEFORE UPDATE OR DELETE ON studio_account_appeals FOR EACH ROW EXECUTE FUNCTION studio_account_appeal_guard();
CREATE FUNCTION studio_account_plan_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.consumed_at IS NOT NULL THEN RAISE EXCEPTION 'executed account plan cannot be deleted'; END IF;
  RETURN OLD;
 END IF;
 IF (NEW.id,NEW.target_user_id,NEW.actor_id,NEW.action,NEW.snapshot,NEW.digest,NEW.expires_at,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.target_user_id,OLD.actor_id,OLD.action,OLD.snapshot,OLD.digest,OLD.expires_at,OLD.created_at) OR OLD.consumed_at IS NOT NULL OR NEW.consumed_at IS NULL THEN RAISE EXCEPTION 'account plan is immutable except one consumption'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_account_plan_guard BEFORE UPDATE OR DELETE ON studio_account_plans FOR EACH ROW EXECUTE FUNCTION studio_account_plan_guard();
CREATE TRIGGER studio_account_appeal_idempotency_immutable BEFORE UPDATE OR DELETE ON studio_account_appeal_idempotency FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_account_receipt_immutable BEFORE UPDATE OR DELETE ON studio_account_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
