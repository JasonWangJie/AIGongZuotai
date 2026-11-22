CREATE TABLE studio_collaboration_members (
 project_id text NOT NULL, workspace_id text NOT NULL, target_user_id text NOT NULL REFERENCES studio_users(id),
 invitation_id text NOT NULL UNIQUE CHECK(invitation_id~'^pci_[a-f0-9]{32}$'),
 inviter_id text NOT NULL REFERENCES studio_users(id) CHECK(inviter_id<>target_user_id),
 role text NOT NULL CHECK(role IN('viewer','editor')),
 state text NOT NULL CHECK(state IN('PENDING','ACTIVE','WITHDRAWN','REVOKED')),
 round bigint NOT NULL CHECK(round BETWEEN 1 AND 128), version bigint NOT NULL CHECK(version BETWEEN 1 AND 512),
 expires_at timestamptz NOT NULL,updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(project_id,target_user_id),FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_collaboration_project_live ON studio_collaboration_members(project_id,state,expires_at,target_user_id) WHERE state IN ('ACTIVE','PENDING');
CREATE INDEX studio_collaboration_user_live ON studio_collaboration_members(target_user_id,state,expires_at,project_id) WHERE state IN ('ACTIVE','PENDING');
CREATE INDEX studio_collaboration_project_recent ON studio_collaboration_members(project_id,updated_at DESC,target_user_id);
CREATE TABLE studio_collaboration_receipts (
 actor_id text NOT NULL REFERENCES studio_users(id),operation text NOT NULL CHECK(operation IN('invite','accept','withdraw','downgrade','revoke')),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 project_id text NOT NULL,target_user_id text NOT NULL,version bigint NOT NULL,
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND octet_length(response::text)<=8192),
 PRIMARY KEY(actor_id,operation,key_hash),UNIQUE(project_id,target_user_id,version),
 FOREIGN KEY(project_id,target_user_id) REFERENCES studio_collaboration_members(project_id,target_user_id)
);
CREATE TABLE studio_collaboration_draft_keys (
 workspace_id text NOT NULL,project_id text NOT NULL,actor_id text NOT NULL REFERENCES studio_users(id),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),revision bigint NOT NULL,
 PRIMARY KEY(project_id,actor_id,key_hash),FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 FOREIGN KEY(project_id,revision) REFERENCES studio_draft_revisions(project_id,revision)
);
CREATE TRIGGER studio_collaboration_receipt_immutable BEFORE UPDATE OR DELETE ON studio_collaboration_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_collaboration_draft_key_immutable BEFORE UPDATE OR DELETE ON studio_collaboration_draft_keys FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE FUNCTION studio_collaboration_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'membership history is retained'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'PENDING' OR NEW.round<>1 OR NEW.version<>1 THEN RAISE EXCEPTION 'first invitation required'; END IF;
 ELSIF (NEW.project_id,NEW.workspace_id,NEW.target_user_id) IS DISTINCT FROM (OLD.project_id,OLD.workspace_id,OLD.target_user_id)
    OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'membership version mismatch';
 ELSIF NEW.round<>OLD.round THEN
  IF NEW.round<>OLD.round+1 OR NEW.invitation_id=OLD.invitation_id OR NEW.state<>'PENDING'
    OR (OLD.state NOT IN('WITHDRAWN','REVOKED') AND NOT(OLD.state='PENDING' AND OLD.expires_at<=clock_timestamp()))
  THEN RAISE EXCEPTION 'invalid reinvitation'; END IF;
 ELSIF (NEW.invitation_id,NEW.inviter_id,NEW.expires_at) IS DISTINCT FROM (OLD.invitation_id,OLD.inviter_id,OLD.expires_at)
   OR NOT ((OLD.state='PENDING' AND NEW.state IN('ACTIVE','WITHDRAWN') AND NEW.role=OLD.role)
         OR (OLD.state='ACTIVE' AND NEW.state='REVOKED' AND NEW.role=OLD.role)
         OR (OLD.state='ACTIVE' AND NEW.state='ACTIVE' AND OLD.role='editor' AND NEW.role='viewer'))
 THEN RAISE EXCEPTION 'invalid membership transition'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_collaboration_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_collaboration_members FOR EACH ROW EXECUTE FUNCTION studio_collaboration_guard();
