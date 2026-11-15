-- CREDIT is an internal entitlement, not a cash currency. Historical resources
-- are deliberately not backfilled with reservations or charges.
CREATE TABLE studio_ledger_wallets (
 workspace_id text PRIMARY KEY REFERENCES studio_workspaces(id),
 charged_micros bigint NOT NULL DEFAULT 0 CHECK(charged_micros BETWEEN 0 AND 9000000000000000),
 refunded_micros bigint NOT NULL DEFAULT 0 CHECK(refunded_micros BETWEEN 0 AND charged_micros),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE studio_ledger_accounts (
 id text PRIMARY KEY,
 workspace_id text REFERENCES studio_workspaces(id),
 kind text NOT NULL CHECK(kind IN('available','reserved','issuance','revenue')),
 balance_micros bigint NOT NULL DEFAULT 0 CHECK(balance_micros BETWEEN -9000000000000000 AND 9000000000000000),
 CHECK((workspace_id IS NOT NULL AND kind IN('available','reserved') AND balance_micros>=0) OR (workspace_id IS NULL AND kind IN('issuance','revenue'))),
 UNIQUE(workspace_id,kind)
);
INSERT INTO studio_ledger_accounts(id,kind) VALUES('system:issuance','issuance'),('system:revenue','revenue');
CREATE TABLE studio_ledger_quotes (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 project_id text NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN('run','task')),
 conversation_id text NOT NULL DEFAULT '',
 model_id text NOT NULL, model_version text NOT NULL, price_version text NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 price jsonb NOT NULL,
 reserve_micros bigint NOT NULL CHECK(reserve_micros BETWEEN 1 AND 9000000000000000),
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reservation_id text UNIQUE,
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_ledger_quotes_active ON studio_ledger_quotes(workspace_id,expires_at) WHERE reservation_id IS NULL;
CREATE TABLE studio_ledger_reservations (
 id text PRIMARY KEY,
 quote_id text NOT NULL UNIQUE REFERENCES studio_ledger_quotes(id),
 workspace_id text NOT NULL REFERENCES studio_ledger_wallets(workspace_id),
 project_id text NOT NULL,
 resource_type text NOT NULL CHECK(resource_type IN('run','task')),
 resource_id text NOT NULL,
 model_id text NOT NULL,
 price_version text NOT NULL,
 price jsonb NOT NULL,
 state text NOT NULL DEFAULT 'HELD' CHECK(state IN('HELD','REVIEW','SETTLED','RELEASED','REFUNDED')),
 reserved_micros bigint NOT NULL CHECK(reserved_micros BETWEEN 1 AND 9000000000000000),
 charged_micros bigint NOT NULL DEFAULT 0 CHECK(charged_micros>=0),
 released_micros bigint NOT NULL DEFAULT 0 CHECK(released_micros>=0),
 refunded_micros bigint NOT NULL DEFAULT 0 CHECK(refunded_micros>=0 AND refunded_micros<=charged_micros),
 started boolean NOT NULL DEFAULT false,
 review_reason text NOT NULL DEFAULT '',
 usage jsonb,
 charge_journal_id text,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(charged_micros+released_micros<=reserved_micros),
 CHECK(state IN('HELD','REVIEW') OR charged_micros+released_micros=reserved_micros),
 UNIQUE(resource_type,resource_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
ALTER TABLE studio_ledger_quotes ADD CONSTRAINT studio_ledger_quote_reservation FOREIGN KEY(reservation_id) REFERENCES studio_ledger_reservations(id);
CREATE INDEX studio_ledger_reservations_page ON studio_ledger_reservations(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_ledger_reservations_project ON studio_ledger_reservations(workspace_id,project_id,created_at DESC,id DESC);
CREATE INDEX studio_ledger_reservations_open ON studio_ledger_reservations(workspace_id,state) WHERE state IN('HELD','REVIEW');
CREATE TABLE studio_ledger_journals (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES studio_ledger_wallets(workspace_id),
 project_id text,
 kind text NOT NULL CHECK(kind IN('CREDIT','RESERVE','CHARGE','RELEASE','REFUND','REFUND_RELEASE')),
 resource_type text, resource_id text,
 reservation_id text REFERENCES studio_ledger_reservations(id),
 quote_id text REFERENCES studio_ledger_quotes(id),
 reversal_of text UNIQUE REFERENCES studio_ledger_journals(id),
 amount_micros bigint NOT NULL CHECK(amount_micros BETWEEN 1 AND 9000000000000000),
 available_delta_micros bigint NOT NULL,
 reserved_delta_micros bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX studio_ledger_journals_page ON studio_ledger_journals(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_ledger_journals_project ON studio_ledger_journals(workspace_id,project_id,created_at DESC,id DESC);
CREATE TABLE studio_ledger_postings (
 journal_id text NOT NULL REFERENCES studio_ledger_journals(id),
 account_id text NOT NULL REFERENCES studio_ledger_accounts(id),
 delta_micros bigint NOT NULL CHECK(delta_micros<>0 AND delta_micros BETWEEN -9000000000000000 AND 9000000000000000),
 PRIMARY KEY(journal_id,account_id)
);
CREATE INDEX studio_ledger_postings_account ON studio_ledger_postings(account_id);
CREATE FUNCTION studio_ledger_no_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger records are immutable'; END $$;
CREATE TRIGGER studio_ledger_journal_immutable BEFORE UPDATE OR DELETE ON studio_ledger_journals FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_ledger_posting_immutable BEFORE UPDATE OR DELETE ON studio_ledger_postings FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE FUNCTION studio_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target text; n bigint; balance numeric;
BEGIN
 IF TG_TABLE_NAME='studio_ledger_journals' THEN target:=NEW.id; ELSE target:=NEW.journal_id; END IF;
 SELECT count(*),COALESCE(sum(delta_micros),0) INTO n,balance FROM studio_ledger_postings WHERE journal_id=target;
 IF n<2 OR balance<>0 THEN RAISE EXCEPTION 'unbalanced ledger journal'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER studio_ledger_journal_balanced AFTER INSERT ON studio_ledger_journals DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_ledger_balanced();
CREATE CONSTRAINT TRIGGER studio_ledger_posting_balanced AFTER INSERT ON studio_ledger_postings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_ledger_balanced();
CREATE TABLE studio_ledger_operations (
 operation_key text PRIMARY KEY,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 operation_id text NOT NULL UNIQUE,
 purpose text NOT NULL CHECK(purpose IN('credit','refund','resolve')),
 reason text NOT NULL, evidence_ref text NOT NULL,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER studio_ledger_operation_immutable BEFORE UPDATE OR DELETE ON studio_ledger_operations FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
