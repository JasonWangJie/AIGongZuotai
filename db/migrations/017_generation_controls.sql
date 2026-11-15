-- C1 controls only future acceptance. No content decision or billing change.
ALTER TABLE studio_operations_grants DROP CONSTRAINT studio_operations_grants_role_check;
ALTER TABLE studio_operations_grants ADD CHECK(role IN('observer','operator','finance','moderator'));
ALTER TABLE studio_operations_audit DROP CONSTRAINT studio_operations_audit_action_check;
ALTER TABLE studio_operations_audit ADD CHECK(action IN('role.granted','role.revoked','case.created','case.updated','case.previewed','case.executed','case.reconciled','case.resolved','case.refunded','generation.previewed','generation.restricted','generation.lifted','appeal.created','appeal.upheld','appeal.lifted'));
ALTER TABLE studio_operations_audit DROP CONSTRAINT studio_operations_audit_resource_type_check;
ALTER TABLE studio_operations_audit ADD CHECK(resource_type IN('','run','task','reservation','grant','case','restriction','appeal'));

CREATE TABLE studio_generation_restrictions (
 id text PRIMARY KEY CHECK(id ~ '^grst_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 reason_code text NOT NULL CHECK(reason_code IN('manual_review','policy_concern','abuse_concern')),
 public_notice text NOT NULL CHECK(octet_length(public_notice) BETWEEN 1 AND 1024),
 evidence_ref text NOT NULL CHECK(octet_length(evidence_ref) BETWEEN 1 AND 128),
 created_by text NOT NULL REFERENCES studio_users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 released_at timestamptz,
 UNIQUE(id,workspace_id),
 CHECK(released_at IS NULL OR released_at>=created_at)
);
CREATE UNIQUE INDEX studio_generation_restriction_active ON studio_generation_restrictions(workspace_id) WHERE released_at IS NULL;
CREATE INDEX studio_generation_restrictions_page ON studio_generation_restrictions(workspace_id,created_at DESC,id DESC);
CREATE TABLE studio_generation_controls (
 workspace_id text PRIMARY KEY REFERENCES studio_workspaces(id),
 state text NOT NULL CHECK(state IN('OPEN','RESTRICTED')),
 version bigint NOT NULL CHECK(version BETWEEN 2 AND 9007199254740991),
 active_restriction_id text,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(active_restriction_id,workspace_id) REFERENCES studio_generation_restrictions(id,workspace_id),
 CHECK((state='OPEN' AND active_restriction_id IS NULL) OR (state='RESTRICTED' AND active_restriction_id IS NOT NULL AND version<9007199254740991))
);
CREATE TABLE studio_generation_appeals (
 id text PRIMARY KEY CHECK(id ~ '^gapl_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
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
 UNIQUE(id,workspace_id,applicant_id),
 UNIQUE(restriction_id,applicant_id),
 FOREIGN KEY(restriction_id,workspace_id) REFERENCES studio_generation_restrictions(id,workspace_id),
 CHECK((state='OPEN' AND outcome='' AND version=1 AND public_reply='') OR (state='RESOLVED' AND outcome IN('UPHELD','LIFTED') AND version=2 AND octet_length(public_reply)>0))
);
CREATE INDEX studio_generation_appeals_user_page ON studio_generation_appeals(workspace_id,applicant_id,created_at DESC,id DESC);
CREATE INDEX studio_generation_appeals_scope_page ON studio_generation_appeals(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_generation_appeals_open ON studio_generation_appeals(workspace_id,restriction_id,id) WHERE state='OPEN';
CREATE TABLE studio_generation_appeal_idempotency (
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 applicant_id text NOT NULL REFERENCES studio_users(id),
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 appeal_id text NOT NULL,
 PRIMARY KEY(workspace_id,applicant_id,idempotency_key),
 FOREIGN KEY(appeal_id,workspace_id,applicant_id) REFERENCES studio_generation_appeals(id,workspace_id,applicant_id)
);
CREATE TABLE studio_generation_plans (
 id text PRIMARY KEY CHECK(id ~ '^gplan_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 actor_id text NOT NULL REFERENCES studio_users(id),
 action text NOT NULL CHECK(action IN('restrict_future','lift_restriction','uphold_restriction')),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 digest text NOT NULL CHECK(digest ~ '^[a-f0-9]{64}$'),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 consumed_at timestamptz,
 UNIQUE(id,workspace_id)
);
CREATE INDEX studio_generation_plans_active ON studio_generation_plans(workspace_id,expires_at) WHERE consumed_at IS NULL;
CREATE INDEX studio_generation_plans_cleanup ON studio_generation_plans(expires_at,id) WHERE consumed_at IS NULL;
CREATE UNIQUE INDEX studio_generation_lift_plan_pending ON studio_generation_plans(workspace_id) WHERE action='lift_restriction' AND consumed_at IS NULL;
CREATE TABLE studio_generation_receipts (
 id text PRIMARY KEY CHECK(id ~ '^grec_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 plan_id text NOT NULL UNIQUE,
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(plan_id,workspace_id) REFERENCES studio_generation_plans(id,workspace_id)
);
CREATE INDEX studio_generation_receipts_scope ON studio_generation_receipts(workspace_id,created_at DESC,id DESC);

CREATE FUNCTION studio_generation_control_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'generation control history cannot be deleted'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.version<>2 OR NEW.state<>'RESTRICTED' THEN RAISE EXCEPTION 'first generation control must restrict at version 2'; END IF;
 ELSE
  IF NEW.workspace_id<>OLD.workspace_id OR NEW.version<>OLD.version+1 OR NEW.state=OLD.state THEN RAISE EXCEPTION 'generation control transition invalid'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_generation_control_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_generation_controls FOR EACH ROW EXECUTE FUNCTION studio_generation_control_guard();
CREATE FUNCTION studio_generation_restriction_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'generation restriction history cannot be deleted'; END IF;
 IF (NEW.id,NEW.workspace_id,NEW.reason_code,NEW.public_notice,NEW.evidence_ref,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.reason_code,OLD.public_notice,OLD.evidence_ref,OLD.created_by,OLD.created_at) OR OLD.released_at IS NOT NULL OR NEW.released_at IS NULL THEN RAISE EXCEPTION 'restriction origin immutable; release is one way'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_generation_restriction_guard BEFORE UPDATE OR DELETE ON studio_generation_restrictions FOR EACH ROW EXECUTE FUNCTION studio_generation_restriction_guard();
-- Deferred: create or release updates both sides in one transaction/savepoint.
CREATE FUNCTION studio_generation_active_consistency() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE ws text; BEGIN
 ws:=NEW.workspace_id;
 IF EXISTS(SELECT 1 FROM studio_generation_restrictions r LEFT JOIN studio_generation_controls c ON c.workspace_id=r.workspace_id WHERE r.workspace_id=ws AND r.released_at IS NULL AND (c.state IS DISTINCT FROM 'RESTRICTED' OR c.active_restriction_id IS DISTINCT FROM r.id)) OR EXISTS(SELECT 1 FROM studio_generation_controls c LEFT JOIN studio_generation_restrictions r ON r.id=c.active_restriction_id AND r.workspace_id=c.workspace_id WHERE c.workspace_id=ws AND c.state='RESTRICTED' AND (r.id IS NULL OR r.released_at IS NOT NULL)) THEN RAISE EXCEPTION 'generation control and active restriction disagree'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER studio_generation_control_consistency AFTER INSERT OR UPDATE ON studio_generation_controls DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_generation_active_consistency();
CREATE CONSTRAINT TRIGGER studio_generation_restriction_consistency AFTER INSERT OR UPDATE ON studio_generation_restrictions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_generation_active_consistency();
CREATE FUNCTION studio_generation_appeal_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'generation appeal history cannot be deleted'; END IF;
 IF (NEW.id,NEW.workspace_id,NEW.restriction_id,NEW.applicant_id,NEW.reason_code,NEW.statement,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.restriction_id,OLD.applicant_id,OLD.reason_code,OLD.statement,OLD.created_at) OR OLD.state<>'OPEN' OR NEW.state<>'RESOLVED' OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'generation appeal transition invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_generation_appeal_guard BEFORE UPDATE OR DELETE ON studio_generation_appeals FOR EACH ROW EXECUTE FUNCTION studio_generation_appeal_guard();
CREATE FUNCTION studio_generation_plan_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.consumed_at IS NOT NULL THEN RAISE EXCEPTION 'executed generation plan cannot be deleted'; END IF;
  RETURN OLD;
 END IF;
 IF (NEW.id,NEW.workspace_id,NEW.actor_id,NEW.action,NEW.snapshot,NEW.digest,NEW.expires_at,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.actor_id,OLD.action,OLD.snapshot,OLD.digest,OLD.expires_at,OLD.created_at) OR OLD.consumed_at IS NOT NULL OR NEW.consumed_at IS NULL THEN RAISE EXCEPTION 'generation plan is immutable except one consumption'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_generation_plan_guard BEFORE UPDATE OR DELETE ON studio_generation_plans FOR EACH ROW EXECUTE FUNCTION studio_generation_plan_guard();
CREATE TRIGGER studio_generation_appeal_idempotency_immutable BEFORE UPDATE OR DELETE ON studio_generation_appeal_idempotency FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_generation_receipt_immutable BEFORE UPDATE OR DELETE ON studio_generation_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
