-- No migration issues a checkout binding, accepts a payment, or creates credit.
CREATE TABLE studio_order_payment_environment (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 environment text UNIQUE NOT NULL CHECK(environment IN ('test','live'))
);
CREATE TABLE studio_order_payment_bindings (
 order_id text PRIMARY KEY, owner_user_id text NOT NULL, workspace_id text NOT NULL,
 source text NOT NULL CHECK(source ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 merchant text NOT NULL CHECK(merchant ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 environment text NOT NULL REFERENCES studio_order_payment_environment(environment),
 bound_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(order_id,source,merchant,environment),
 FOREIGN KEY(order_id,owner_user_id,workspace_id) REFERENCES studio_order_intents(id,owner_user_id,workspace_id)
);
-- Multiple delivery notifications can describe a single economic fact. A
-- transaction may first appear on a refund. Conflicting identities stay flagged.
CREATE TABLE studio_order_payment_identities (
 source text NOT NULL,merchant text NOT NULL,environment text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('transaction','refund')),
 external_id text NOT NULL CHECK(external_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 order_id text NOT NULL,transaction_id text NOT NULL CHECK(transaction_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 disputed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(source,merchant,environment,kind,external_id),
 FOREIGN KEY(order_id,source,merchant,environment) REFERENCES studio_order_payment_bindings(order_id,source,merchant,environment),
 CHECK(kind<>'transaction' OR external_id=transaction_id)
);
CREATE FUNCTION studio_payment_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.disputed OR NOT NEW.disputed OR
   (to_jsonb(OLD)-'disputed')<>(to_jsonb(NEW)-'disputed') THEN RAISE EXCEPTION 'payment identity is immutable except for quarantine'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_payment_identity_guard BEFORE UPDATE OR DELETE ON studio_order_payment_identities FOR EACH ROW EXECUTE FUNCTION studio_payment_identity_guard();

CREATE TABLE studio_order_payment_facts (
 order_id text NOT NULL REFERENCES studio_order_payment_bindings(order_id),
 kind text NOT NULL CHECK(kind IN ('payment','refund')),
 external_id text NOT NULL CHECK(external_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 transaction_id text NOT NULL CHECK(transaction_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 amount_minor bigint NOT NULL CHECK(amount_minor BETWEEN 1 AND 1000000000000),
 occurred_at timestamptz NOT NULL, nano_remainder smallint NOT NULL CHECK(nano_remainder BETWEEN 0 AND 999),
 first_received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 fact_hash bytea NOT NULL CHECK(octet_length(fact_hash)=32),
 PRIMARY KEY(order_id,kind,external_id),CHECK(kind<>'payment' OR transaction_id=external_id)
);
CREATE TABLE studio_order_payment_events (
 source text NOT NULL,merchant text NOT NULL,environment text NOT NULL,
 event_id text NOT NULL CHECK(event_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
 order_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('payment','refund')),
 external_id text NOT NULL, transaction_id text NOT NULL,
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),amount_minor bigint NOT NULL CHECK(amount_minor BETWEEN 1 AND 1000000000000),
 occurred_at timestamptz NOT NULL,nano_remainder smallint NOT NULL CHECK(nano_remainder BETWEEN 0 AND 999),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 receipt jsonb NOT NULL CHECK(jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=4096),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source,merchant,environment,event_id),
 FOREIGN KEY(order_id,source,merchant,environment) REFERENCES studio_order_payment_bindings(order_id,source,merchant,environment),
 FOREIGN KEY(order_id,kind,external_id) REFERENCES studio_order_payment_facts(order_id,kind,external_id)
);
CREATE INDEX studio_order_payment_events_order ON studio_order_payment_events(order_id);
CREATE TABLE studio_order_payment_conflicts (
 source text NOT NULL,merchant text NOT NULL,environment text NOT NULL,event_id text NOT NULL,
 claimed_order_id text NOT NULL,request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 observation jsonb NOT NULL CHECK(jsonb_typeof(observation)='object' AND octet_length(observation::text)<=4096),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source,merchant,environment,event_id,request_hash),
 FOREIGN KEY(source,merchant,environment,event_id) REFERENCES studio_order_payment_events(source,merchant,environment,event_id),
 FOREIGN KEY(claimed_order_id,source,merchant,environment) REFERENCES studio_order_payment_bindings(order_id,source,merchant,environment)
);
CREATE TABLE studio_order_payment_credits (
 order_id text PRIMARY KEY REFERENCES studio_order_payment_bindings(order_id),
 journal_id text UNIQUE NOT NULL REFERENCES studio_ledger_journals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- The journal identifies its original purchase and has two balanced CREDIT postings.
CREATE UNIQUE INDEX studio_order_purchase_credit_journal ON studio_ledger_journals(resource_id)
 WHERE kind='CREDIT' AND resource_type='order';
CREATE FUNCTION studio_order_purchase_credit_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM studio_order_intents o JOIN studio_ledger_journals j ON j.id=NEW.journal_id
   WHERE o.id=NEW.order_id AND j.workspace_id=o.workspace_id AND j.resource_type='order' AND j.resource_id=o.id
   AND j.kind='CREDIT' AND j.amount_micros=o.credit_micros AND j.available_delta_micros=o.credit_micros AND j.reserved_delta_micros=0
   AND (SELECT count(*) FROM studio_ledger_postings p WHERE p.journal_id=j.id)=2
   AND EXISTS(SELECT 1 FROM studio_ledger_postings p WHERE p.journal_id=j.id AND p.account_id='system:issuance' AND p.delta_micros=-o.credit_micros)
   AND EXISTS(SELECT 1 FROM studio_ledger_postings p WHERE p.journal_id=j.id AND p.account_id='available:'||o.workspace_id AND p.delta_micros=o.credit_micros))
 THEN RAISE EXCEPTION 'purchase credit does not match frozen order'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_order_purchase_credit_guard BEFORE INSERT ON studio_order_payment_credits FOR EACH ROW EXECUTE FUNCTION studio_order_purchase_credit_guard();
CREATE TABLE studio_order_payment_reviews (
 order_id text NOT NULL REFERENCES studio_order_payment_bindings(order_id),
 reason text NOT NULL CHECK(reason IN ('credit_capacity','event_conflict','refund_conflict')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(order_id,reason)
);
CREATE TRIGGER studio_payment_conflict_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_conflicts FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_environment_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_environment FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_binding_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_bindings FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_fact_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_facts FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_event_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_events FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_credit_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_credits FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();
CREATE TRIGGER studio_payment_review_immutable BEFORE UPDATE OR DELETE ON studio_order_payment_reviews FOR EACH ROW EXECUTE FUNCTION studio_ledger_no_mutation();

-- The 033 owner limit counts unresolved intentions. A bounded lookup must not
-- walk an owner's lifetime of paid orders, whose original state stays PENDING.
CREATE TABLE studio_order_pending_slots (
 owner_user_id text NOT NULL REFERENCES studio_users(id),
 order_id text PRIMARY KEY REFERENCES studio_order_intents(id)
);
CREATE INDEX studio_order_pending_slots_owner ON studio_order_pending_slots(owner_user_id);
INSERT INTO studio_order_pending_slots(owner_user_id,order_id)
 SELECT owner_user_id,id FROM studio_order_intents WHERE state='PENDING';
CREATE FUNCTION studio_order_pending_slot_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='studio_order_payment_credits' THEN
  DELETE FROM studio_order_pending_slots WHERE order_id=NEW.order_id;
 ELSIF TG_OP='INSERT' THEN
  INSERT INTO studio_order_pending_slots(owner_user_id,order_id) VALUES(NEW.owner_user_id,NEW.id);
 ELSE
  DELETE FROM studio_order_pending_slots WHERE order_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_order_intent_pending_slot AFTER INSERT OR UPDATE OF state ON studio_order_intents FOR EACH ROW EXECUTE FUNCTION studio_order_pending_slot_change();
CREATE TRIGGER studio_order_credit_pending_slot AFTER INSERT ON studio_order_payment_credits FOR EACH ROW EXECUTE FUNCTION studio_order_pending_slot_change();
