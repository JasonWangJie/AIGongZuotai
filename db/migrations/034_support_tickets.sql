ALTER TABLE studio_operations_grants DROP CONSTRAINT studio_operations_grants_role_check;
ALTER TABLE studio_operations_grants ADD CHECK(role IN('observer','operator','finance','moderator','risk_reviewer','support'));

CREATE TABLE studio_support_tickets (
 id text PRIMARY KEY CHECK(id~'^tkt_[a-f0-9]{32}$'),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 owner_user_id text NOT NULL REFERENCES studio_users(id),
 category text NOT NULL CHECK(category IN('account','billing','generation','other')),
 subject text NOT NULL CHECK(char_length(subject) BETWEEN 1 AND 120 AND octet_length(subject)<=480),
 state text NOT NULL CHECK(state IN('OPEN','CLOSED')),
 version integer NOT NULL CHECK(version BETWEEN 1 AND 32),
 created_at timestamptz NOT NULL,updated_at timestamptz NOT NULL CHECK(updated_at>=created_at),
 CHECK((state='OPEN' AND version<=31) OR (state='CLOSED' AND version>=2)),
 UNIQUE(id,workspace_id)
);
CREATE INDEX studio_support_owner_page ON studio_support_tickets(workspace_id,owner_user_id,created_at DESC,id DESC);
CREATE INDEX studio_support_staff_page ON studio_support_tickets(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_support_open_count ON studio_support_tickets(workspace_id,owner_user_id) WHERE state='OPEN';

CREATE TABLE studio_support_messages (
 ticket_id text NOT NULL, workspace_id text NOT NULL,
 version integer NOT NULL CHECK(version BETWEEN 1 AND 32),
 actor_user_id text NOT NULL REFERENCES studio_users(id),
 operation text NOT NULL CHECK(operation IN('create','followup','reply','close')),
 body text NOT NULL,created_at timestamptz NOT NULL,
 PRIMARY KEY(ticket_id,version),
 UNIQUE(ticket_id,workspace_id,version,actor_user_id,operation),
 FOREIGN KEY(ticket_id,workspace_id) REFERENCES studio_support_tickets(id,workspace_id),
 CHECK((version=1)=(operation='create')),
 CHECK((operation='close' AND body='') OR (operation<>'close' AND version<32
       AND char_length(body) BETWEEN 1 AND 2000 AND octet_length(body)<=6000))
);
CREATE TABLE studio_support_receipts (
 workspace_id text NOT NULL,actor_user_id text NOT NULL,operation text NOT NULL,
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 ticket_id text NOT NULL,version integer NOT NULL,
 PRIMARY KEY(workspace_id,actor_user_id,operation,key_hash),
 UNIQUE(ticket_id,version),
 FOREIGN KEY(ticket_id,workspace_id,version,actor_user_id,operation)
   REFERENCES studio_support_messages(ticket_id,workspace_id,version,actor_user_id,operation)
);

CREATE FUNCTION studio_support_ticket_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'support ticket history is immutable'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'OPEN' OR NEW.version<>1 OR NEW.updated_at<>NEW.created_at THEN RAISE EXCEPTION 'invalid new ticket'; END IF;
 ELSIF (NEW.id,NEW.workspace_id,NEW.owner_user_id,NEW.category,NEW.subject,NEW.created_at) IS DISTINCT FROM
       (OLD.id,OLD.workspace_id,OLD.owner_user_id,OLD.category,OLD.subject,OLD.created_at)
       OR OLD.state<>'OPEN' OR NEW.version<>OLD.version+1 OR NEW.updated_at<OLD.updated_at THEN
  RAISE EXCEPTION 'support ticket transition invalid';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_support_ticket_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_support_tickets FOR EACH ROW EXECUTE FUNCTION studio_support_ticket_guard();
CREATE TRIGGER studio_support_message_immutable BEFORE UPDATE OR DELETE ON studio_support_messages FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_support_receipt_immutable BEFORE UPDATE OR DELETE ON studio_support_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();

-- A version consists of one visible message and its actor's receipt. Reads
-- reconstruct old receipts from immutable messages without copying past text.
CREATE FUNCTION studio_support_version_complete() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target text; head studio_support_tickets%ROWTYPE;
BEGIN
 IF TG_TABLE_NAME='studio_support_tickets' THEN target:=NEW.id; ELSE target:=NEW.ticket_id; END IF;
 SELECT * INTO STRICT head FROM studio_support_tickets WHERE id=target;
 IF (SELECT count(*) FROM studio_support_messages WHERE ticket_id=target)<>head.version
    OR (SELECT count(*) FROM studio_support_receipts WHERE ticket_id=target)<>head.version
    OR NOT EXISTS (SELECT 1 FROM studio_support_messages WHERE ticket_id=target AND version=head.version
                   AND (operation='close')=(head.state='CLOSED') AND created_at=head.updated_at)
    OR EXISTS (SELECT 1 FROM studio_support_messages WHERE ticket_id=target
               AND (version>head.version OR (operation<>'reply' AND actor_user_id<>head.owner_user_id)
                   OR (operation='close' AND version<>head.version))) THEN
  RAISE EXCEPTION 'incomplete support ticket version';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER studio_support_head_complete AFTER INSERT OR UPDATE ON studio_support_tickets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_support_version_complete();
CREATE CONSTRAINT TRIGGER studio_support_message_complete AFTER INSERT ON studio_support_messages DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_support_version_complete();
