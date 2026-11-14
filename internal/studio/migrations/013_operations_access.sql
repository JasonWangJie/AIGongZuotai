-- Operators are ordinary authenticated users with explicit, scoped grants.
-- Registration, workspace ownership and email verification grant no ops access.
CREATE TABLE studio_operations_grants (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES studio_users(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 role text NOT NULL CHECK(role IN ('observer','operator','finance')),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version BETWEEN 1 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(user_id,workspace_id,role)
);
CREATE INDEX studio_operations_grants_user ON studio_operations_grants(user_id,workspace_id,role) WHERE active;
CREATE INDEX studio_operations_grants_workspace ON studio_operations_grants(workspace_id,user_id) WHERE active;
CREATE TABLE studio_operations_audit (
 id text PRIMARY KEY,
 actor_user_id text REFERENCES studio_users(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 action text NOT NULL CHECK(action IN ('role.granted','role.revoked','case.created','case.updated','case.previewed','case.executed','case.reconciled','case.resolved','case.refunded')),
 case_id text NOT NULL DEFAULT '',
 resource_type text NOT NULL DEFAULT '' CHECK(resource_type IN ('','run','task','reservation','grant','case')),
 resource_id text NOT NULL DEFAULT '',
 operation_id text NOT NULL DEFAULT '',
 reason_code text NOT NULL DEFAULT '',
 evidence_ref text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 0 CHECK(version BETWEEN 0 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX studio_operations_audit_scope ON studio_operations_audit(workspace_id,created_at DESC,id DESC);
CREATE INDEX studio_operations_audit_case ON studio_operations_audit(workspace_id,case_id,created_at DESC,id DESC);
CREATE TABLE studio_operations_role_operations (
 operation_key text PRIMARY KEY,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 operation_id text NOT NULL UNIQUE,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION studio_operations_no_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'operations evidence is immutable'; END $$;
CREATE TRIGGER studio_operations_audit_immutable BEFORE UPDATE OR DELETE ON studio_operations_audit FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_operations_role_operation_immutable BEFORE UPDATE OR DELETE ON studio_operations_role_operations FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
