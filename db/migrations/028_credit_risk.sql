-- The latest explicit workspace revision is active. No row means disabled.
CREATE TABLE studio_credit_risk_policies(
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 enabled boolean NOT NULL,max_held_micros bigint NOT NULL,max_per_minute integer NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(workspace_id,version),
 CHECK((enabled AND max_held_micros BETWEEN 1 AND 9000000000000000 AND max_per_minute BETWEEN 1 AND 1000 AND version<9007199254740991)
 OR (NOT enabled AND max_held_micros=0 AND max_per_minute=0))
);
CREATE FUNCTION studio_credit_risk_policy_append() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE previous bigint; BEGIN
 PERFORM id FROM studio_workspaces WHERE id=NEW.workspace_id FOR NO KEY UPDATE;
 SELECT max(version) INTO previous FROM studio_credit_risk_policies WHERE workspace_id=NEW.workspace_id;
 IF NEW.version<>COALESCE(previous,0)+1 THEN RAISE EXCEPTION 'risk policy revision must advance by one'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_credit_risk_policy_append BEFORE INSERT ON studio_credit_risk_policies FOR EACH ROW EXECUTE FUNCTION studio_credit_risk_policy_append();
CREATE TRIGGER studio_credit_risk_policy_immutable BEFORE UPDATE OR DELETE ON studio_credit_risk_policies FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TABLE studio_credit_risk_policy_operations(
 operation_key text PRIMARY KEY CHECK(octet_length(operation_key) BETWEEN 1 AND 128),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 operation_id text NOT NULL UNIQUE,response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER studio_credit_risk_policy_operation_immutable BEFORE UPDATE OR DELETE ON studio_credit_risk_policy_operations FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();

-- Existing quotes have no provable applicant. They retain their normal price
-- and expiry, but are ineligible for a personal exception.
ALTER TABLE studio_ledger_quotes ADD COLUMN owner_user_id text REFERENCES studio_users(id);
CREATE FUNCTION studio_credit_risk_quote_owner_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id THEN RAISE EXCEPTION 'quote applicant is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_credit_risk_quote_owner_guard BEFORE UPDATE OF owner_user_id ON studio_ledger_quotes FOR EACH ROW EXECUTE FUNCTION studio_credit_risk_quote_owner_guard();
ALTER TABLE studio_operations_grants DROP CONSTRAINT studio_operations_grants_role_check;
ALTER TABLE studio_operations_grants ADD CHECK(role IN('observer','operator','finance','moderator','risk_reviewer'));
ALTER TABLE studio_operations_audit DROP CONSTRAINT studio_operations_audit_action_check,
 ADD CONSTRAINT studio_operations_audit_action_check CHECK(action IN('role.granted','role.revoked','case.created','case.updated','case.previewed','case.executed','case.reconciled','case.resolved','case.refunded','generation.previewed','generation.restricted','generation.lifted','appeal.created','appeal.upheld','appeal.lifted','content_appeal.created','content_appeal.previewed','content_appeal.upheld','content_appeal.opinion','content_appeal.referred','content_appeal.finance_linked','risk_review.created','risk_review.previewed','risk_review.approved','risk_review.denied'));

CREATE TABLE studio_credit_risk_reviews(
 id text PRIMARY KEY CHECK(id~'^rskrev_[a-f0-9]{32}$'),workspace_id text NOT NULL,project_id text NOT NULL,
 applicant_id text NOT NULL REFERENCES studio_users(id),quote_id text NOT NULL REFERENCES studio_ledger_quotes(id),
 policy_version bigint NOT NULL,rule text NOT NULL CHECK(rule IN('HELD_LIMIT','RATE_LIMIT')),
 reason_codes text[] NOT NULL CHECK(reason_codes IN(ARRAY['HELD_LIMIT'],ARRAY['RATE_LIMIT'],ARRAY['HELD_LIMIT','RATE_LIMIT']) AND rule=reason_codes[1]),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),price_version text NOT NULL,reserve_micros bigint NOT NULL CHECK(reserve_micros BETWEEN 1 AND 9000000000000000),
 expires_at timestamptz NOT NULL,reason_code text NOT NULL CHECK(reason_code IN('request_review','limit_error','billing_concern')),
 evidence_digest text NOT NULL CHECK(evidence_digest~'^[a-f0-9]{64}$'),statement text NOT NULL DEFAULT '' CHECK(octet_length(statement)<=1024),
 state text NOT NULL DEFAULT 'OPEN' CHECK(state IN('OPEN','APPROVED','DENIED')),version bigint NOT NULL DEFAULT 1,
 public_reply text NOT NULL DEFAULT '' CHECK(octet_length(public_reply)<=1024),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 FOREIGN KEY(workspace_id,policy_version) REFERENCES studio_credit_risk_policies(workspace_id,version),
 UNIQUE(quote_id,policy_version),UNIQUE(id,workspace_id),UNIQUE(id,applicant_id),
 CHECK((state='OPEN' AND version=1 AND public_reply='') OR (state IN('APPROVED','DENIED') AND version=2 AND octet_length(public_reply)>0))
);
CREATE INDEX studio_credit_risk_review_owner ON studio_credit_risk_reviews(workspace_id,project_id,applicant_id,created_at DESC,id DESC);
CREATE INDEX studio_credit_risk_review_scope ON studio_credit_risk_reviews(workspace_id,state,created_at DESC,id DESC);
CREATE TABLE studio_credit_risk_review_keys(
 applicant_id text NOT NULL REFERENCES studio_users(id),key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 review_id text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(applicant_id,key_hash),FOREIGN KEY(review_id,applicant_id) REFERENCES studio_credit_risk_reviews(id,applicant_id)
);
CREATE TABLE studio_credit_risk_plans(
 id text PRIMARY KEY CHECK(id~'^rskpl_[a-f0-9]{32}$'),review_id text NOT NULL,workspace_id text NOT NULL,actor_id text NOT NULL REFERENCES studio_users(id),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),digest text NOT NULL CHECK(digest~'^[a-f0-9]{64}$'),
 expires_at timestamptz NOT NULL,consumed_at timestamptz,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(review_id,workspace_id) REFERENCES studio_credit_risk_reviews(id,workspace_id),UNIQUE(id,review_id,workspace_id)
);
CREATE UNIQUE INDEX studio_credit_risk_plan_pending ON studio_credit_risk_plans(review_id) WHERE consumed_at IS NULL;
CREATE TABLE studio_credit_risk_receipts(
 id text PRIMARY KEY CHECK(id~'^rskrc_[a-f0-9]{32}$'),review_id text NOT NULL,workspace_id text NOT NULL,plan_id text NOT NULL UNIQUE,
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(plan_id,review_id,workspace_id) REFERENCES studio_credit_risk_plans(id,review_id,workspace_id)
);
CREATE TABLE studio_credit_risk_permits(
 review_id text PRIMARY KEY,workspace_id text NOT NULL,project_id text NOT NULL,applicant_id text NOT NULL REFERENCES studio_users(id),
 quote_id text NOT NULL REFERENCES studio_ledger_quotes(id),policy_version bigint NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),price_version text NOT NULL,
 reserve_micros bigint NOT NULL CHECK(reserve_micros BETWEEN 1 AND 9000000000000000),expires_at timestamptz NOT NULL,
 reservation_id text UNIQUE REFERENCES studio_ledger_reservations(id),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(review_id,workspace_id) REFERENCES studio_credit_risk_reviews(id,workspace_id),
 FOREIGN KEY(workspace_id,policy_version) REFERENCES studio_credit_risk_policies(workspace_id,version),UNIQUE(quote_id,policy_version)
);
CREATE FUNCTION studio_credit_risk_review_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' OR OLD.state<>'OPEN' OR NEW.state NOT IN('APPROVED','DENIED')
 OR (to_jsonb(NEW)-ARRAY['state','version','public_reply','updated_at']) IS DISTINCT FROM
    (to_jsonb(OLD)-ARRAY['state','version','public_reply','updated_at']) THEN RAISE EXCEPTION 'risk review evidence and final decision are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_credit_risk_review_guard BEFORE UPDATE OR DELETE ON studio_credit_risk_reviews FOR EACH ROW EXECUTE FUNCTION studio_credit_risk_review_guard();
CREATE FUNCTION studio_credit_risk_plan_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' AND OLD.consumed_at IS NULL THEN RETURN OLD; END IF;
 IF TG_OP='DELETE' OR OLD.consumed_at IS NOT NULL OR NEW.consumed_at IS NULL OR
   (to_jsonb(NEW)-'consumed_at') IS DISTINCT FROM (to_jsonb(OLD)-'consumed_at')
 THEN RAISE EXCEPTION 'risk review plan can only be consumed once'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_credit_risk_plan_guard BEFORE UPDATE OR DELETE ON studio_credit_risk_plans FOR EACH ROW EXECUTE FUNCTION studio_credit_risk_plan_guard();
CREATE FUNCTION studio_credit_risk_permit_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.reservation_id IS NOT NULL OR NOT EXISTS(SELECT 1 FROM studio_credit_risk_reviews r JOIN studio_ledger_quotes q ON q.id=r.quote_id WHERE r.id=NEW.review_id AND r.state='APPROVED' AND r.workspace_id=NEW.workspace_id AND r.project_id=NEW.project_id AND r.applicant_id=NEW.applicant_id AND r.quote_id=NEW.quote_id AND r.policy_version=NEW.policy_version AND r.request_hash=NEW.request_hash AND r.price_version=NEW.price_version AND r.reserve_micros=NEW.reserve_micros AND r.expires_at=NEW.expires_at AND q.owner_user_id=r.applicant_id AND q.project_id=r.project_id AND q.workspace_id=r.workspace_id AND q.request_hash=r.request_hash AND q.price_version=r.price_version AND q.reserve_micros=r.reserve_micros AND q.expires_at=r.expires_at AND q.reservation_id IS NULL) THEN RAISE EXCEPTION 'risk permit must match the approved original quote'; END IF;
 ELSIF TG_OP='DELETE' OR OLD.reservation_id IS NOT NULL OR NEW.reservation_id IS NULL OR
 (to_jsonb(NEW)-'reservation_id') IS DISTINCT FROM (to_jsonb(OLD)-'reservation_id') OR NOT EXISTS(SELECT 1 FROM studio_ledger_reservations r WHERE r.id=NEW.reservation_id AND r.quote_id=NEW.quote_id) THEN RAISE EXCEPTION 'risk permit may only be consumed once for its own reservation';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_credit_risk_permit_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_credit_risk_permits FOR EACH ROW EXECUTE FUNCTION studio_credit_risk_permit_guard();
CREATE TRIGGER studio_credit_risk_review_key_immutable BEFORE UPDATE OR DELETE ON studio_credit_risk_review_keys FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_credit_risk_receipt_immutable BEFORE UPDATE OR DELETE ON studio_credit_risk_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
