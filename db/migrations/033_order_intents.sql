-- Personal statements of intent only. A row never proves collection, a
-- merchant refund, or delivery of CREDIT. Monetary and entitlement units
-- are frozen independently for the lifetime of the order.
CREATE TABLE studio_order_intents (
 id text PRIMARY KEY CHECK(id~'^ord_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 owner_user_id text NOT NULL REFERENCES studio_users(id),
 product_id text NOT NULL CHECK(product_id~'^[a-z][a-z0-9_-]{0,47}$'),
 product_name text NOT NULL CHECK(char_length(product_name) BETWEEN 1 AND 80),
 catalog_version text NOT NULL CHECK(catalog_version~'^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$'),
 catalog_digest text NOT NULL CHECK(catalog_digest~'^[a-f0-9]{64}$'),
 product_digest text NOT NULL CHECK(product_digest~'^[a-f0-9]{64}$'),
 currency text NOT NULL CHECK(currency~'^[A-Z]{3}$'),
 currency_exponent integer NOT NULL CHECK(currency_exponent BETWEEN 0 AND 3),
 amount_minor bigint NOT NULL CHECK(amount_minor BETWEEN 1 AND 1000000000000),
 credit_micros bigint NOT NULL CHECK(credit_micros BETWEEN 1 AND 1000000000000000),
 state text NOT NULL CHECK(state IN('PENDING','CANCELLED')),version bigint NOT NULL CHECK(version IN(1,2)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),cancelled_at timestamptz,
 CHECK((state='PENDING' AND version=1 AND cancelled_at IS NULL) OR
       (state='CANCELLED' AND version=2 AND cancelled_at IS NOT NULL AND cancelled_at>=created_at)),
 UNIQUE(id,owner_user_id,workspace_id)
);
CREATE INDEX studio_order_intents_owner_page ON studio_order_intents(owner_user_id,workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_order_intents_pending ON studio_order_intents(owner_user_id) WHERE state='PENDING';
CREATE TABLE studio_order_intent_receipts (
 owner_user_id text NOT NULL,workspace_id text NOT NULL,operation text NOT NULL CHECK(operation IN('create','cancel')),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 order_id text NOT NULL,snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=8192
  AND snapshot->>'id' IS NOT DISTINCT FROM order_id
  AND snapshot->>'state' IS NOT DISTINCT FROM CASE WHEN operation='create' THEN 'PENDING' ELSE 'CANCELLED' END),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(owner_user_id,workspace_id,operation,key_hash),UNIQUE(order_id,operation),
 FOREIGN KEY(order_id,owner_user_id,workspace_id) REFERENCES studio_order_intents(id,owner_user_id,workspace_id)
);
CREATE FUNCTION studio_order_intent_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'PENDING' THEN RAISE EXCEPTION 'only a pending order intent can be created'; END IF;
 ELSIF TG_OP='DELETE' THEN RAISE EXCEPTION 'order history is immutable';
 ELSIF (to_jsonb(NEW)-ARRAY['state','version','cancelled_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','version','cancelled_at'])
  OR OLD.state<>'PENDING' OR NEW.state<>'CANCELLED' OR NEW.cancelled_at IS NULL
 THEN RAISE EXCEPTION 'only cancellation of the original pending intent is permitted'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_order_intent_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_order_intents FOR EACH ROW EXECUTE FUNCTION studio_order_intent_guard();
CREATE TRIGGER studio_order_intent_receipt_immutable BEFORE UPDATE OR DELETE ON studio_order_intent_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
