-- Review records are separate from immutable 015/016 output decisions.
ALTER TABLE studio_operations_audit DROP CONSTRAINT studio_operations_audit_action_check,
 ADD CONSTRAINT studio_operations_audit_action_check CHECK(action IN('role.granted','role.revoked','case.created','case.updated','case.previewed','case.executed','case.reconciled','case.resolved','case.refunded','generation.previewed','generation.restricted','generation.lifted','appeal.created','appeal.upheld','appeal.lifted','content_appeal.created','content_appeal.previewed','content_appeal.upheld','content_appeal.opinion','content_appeal.referred','content_appeal.finance_linked'));
CREATE INDEX studio_content_appeal_accepted_actor ON studio_audit_events(workspace_id,project_id,resource_type,resource_id,actor_id)
 WHERE actor_type='user' AND action IN('run.accepted','task.accepted');
CREATE TABLE studio_content_appeals (
 id text PRIMARY KEY,workspace_id text NOT NULL,project_id text NOT NULL,applicant_id text NOT NULL REFERENCES studio_users(id),
 resource_type text NOT NULL CHECK(resource_type IN('run','task')),resource_id text NOT NULL,
 resource_version bigint NOT NULL CHECK(resource_version>=0),moderation_version bigint NOT NULL CHECK(moderation_version>1),
 run_id text GENERATED ALWAYS AS (CASE WHEN resource_type='run' THEN resource_id END) STORED,
 task_id text GENERATED ALWAYS AS (CASE WHEN resource_type='task' THEN resource_id END) STORED,
 decision text NOT NULL CHECK(decision IN('BLOCKED','REVIEW')),decision_reason text NOT NULL,
 policy_id text NOT NULL,policy_version text NOT NULL,policy_digest text NOT NULL CHECK(policy_digest~'^[a-f0-9]{64}$'),
 evidence_digest text NOT NULL CHECK(evidence_digest~'^[a-f0-9]{64}$'),
 reason_code text NOT NULL CHECK(reason_code IN('decision_error','request_review','billing_concern')),
 statement text NOT NULL CHECK(octet_length(statement)<=1024),state text NOT NULL DEFAULT 'OPEN',version integer NOT NULL DEFAULT 1,
 outcome text NOT NULL DEFAULT '',public_reply text NOT NULL DEFAULT '' CHECK(octet_length(public_reply)<=1024),
 reservation_id text REFERENCES studio_ledger_reservations(id),reservation_version bigint NOT NULL DEFAULT 0,remaining_micros bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 FOREIGN KEY(run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 FOREIGN KEY(run_id,moderation_version) REFERENCES studio_moderation_decisions(run_id,version),
 FOREIGN KEY(task_id,moderation_version) REFERENCES studio_media_moderation_decisions(task_id,version),
 UNIQUE(workspace_id,project_id,applicant_id,resource_type,resource_id,moderation_version),
 CHECK((state='OPEN' AND version=1 AND outcome='' AND public_reply='') OR
   (state='RESOLVED' AND version=2 AND outcome IN('UPHELD','OPINION_RECORDED','FINANCE_REFERRED') AND octet_length(public_reply)>0)),
 CHECK((reservation_id IS NULL AND reservation_version=0 AND remaining_micros=0) OR
   (reservation_id IS NOT NULL AND reservation_version>0 AND remaining_micros>0)),CHECK(outcome<>'FINANCE_REFERRED' OR reservation_id IS NOT NULL)
);
CREATE INDEX studio_content_appeals_owner ON studio_content_appeals(workspace_id,project_id,applicant_id,created_at DESC,id DESC);
CREATE INDEX studio_content_appeals_queue ON studio_content_appeals(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_content_appeals_finance ON studio_content_appeals(workspace_id,created_at DESC,id DESC) WHERE outcome='FINANCE_REFERRED';
CREATE INDEX studio_content_appeal_release_journal ON studio_ledger_journals(reservation_id) WHERE kind='RELEASE';
CREATE TABLE studio_content_appeal_keys(
 applicant_id text NOT NULL REFERENCES studio_users(id),workspace_id text NOT NULL,project_id text NOT NULL,key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),appeal_id text NOT NULL UNIQUE REFERENCES studio_content_appeals(id),
 PRIMARY KEY(applicant_id,workspace_id,project_id,key_hash));
CREATE TABLE studio_content_appeal_plans (
 id text PRIMARY KEY,appeal_id text NOT NULL REFERENCES studio_content_appeals(id),workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 actor_id text NOT NULL REFERENCES studio_users(id),snapshot jsonb NOT NULL CHECK(octet_length(snapshot::text)<=16384),
 digest text NOT NULL CHECK(digest~'^[a-f0-9]{64}$'),expires_at timestamptz NOT NULL,consumed_at timestamptz
);
CREATE UNIQUE INDEX studio_content_appeal_pending ON studio_content_appeal_plans(appeal_id) WHERE consumed_at IS NULL;
CREATE TABLE studio_content_appeal_receipts(
 id text PRIMARY KEY,workspace_id text NOT NULL REFERENCES studio_workspaces(id),appeal_id text NOT NULL UNIQUE REFERENCES studio_content_appeals(id),
 plan_id text NOT NULL UNIQUE REFERENCES studio_content_appeal_plans(id),document jsonb NOT NULL CHECK(octet_length(document::text)<=4096));
CREATE TABLE studio_content_appeal_finance_links(
 appeal_id text PRIMARY KEY REFERENCES studio_content_appeals(id),case_id text UNIQUE NOT NULL REFERENCES studio_operations_cases(id),
 reservation_id text NOT NULL REFERENCES studio_ledger_reservations(id),reservation_version bigint NOT NULL CHECK(reservation_version>0),
 created_by text NOT NULL REFERENCES studio_users(id),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),UNIQUE(created_by,key_hash));
CREATE TRIGGER studio_content_appeal_receipt_immutable BEFORE UPDATE OR DELETE ON studio_content_appeal_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_content_appeal_key_immutable BEFORE UPDATE OR DELETE ON studio_content_appeal_keys FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_content_appeal_finance_immutable BEFORE UPDATE OR DELETE ON studio_content_appeal_finance_links FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE FUNCTION studio_content_appeal_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR OLD.state<>'OPEN' OR NEW.state<>'RESOLVED' OR
  (NEW.id,NEW.workspace_id,NEW.project_id,NEW.applicant_id,NEW.resource_type,NEW.resource_id,NEW.resource_version,NEW.moderation_version,NEW.decision,NEW.decision_reason,NEW.policy_id,NEW.policy_version,NEW.policy_digest,NEW.evidence_digest,NEW.reason_code,NEW.statement,NEW.created_at)
  IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.project_id,OLD.applicant_id,OLD.resource_type,OLD.resource_id,OLD.resource_version,OLD.moderation_version,OLD.decision,OLD.decision_reason,OLD.policy_id,OLD.policy_version,OLD.policy_digest,OLD.evidence_digest,OLD.reason_code,OLD.statement,OLD.created_at)
 THEN RAISE EXCEPTION 'content appeal evidence and completed decisions are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_content_appeal_guard BEFORE UPDATE OR DELETE ON studio_content_appeals FOR EACH ROW EXECUTE FUNCTION studio_content_appeal_guard();
