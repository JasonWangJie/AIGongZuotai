CREATE TABLE studio_project_drafts (
 project_id text PRIMARY KEY,
 workspace_id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0 CHECK (revision BETWEEN 0 AND 1000),
 updated_at timestamptz,
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 CHECK ((revision=0 AND updated_at IS NULL) OR (revision>0 AND updated_at IS NOT NULL))
);

CREATE TABLE studio_draft_revisions (
 project_id text NOT NULL,
 workspace_id text NOT NULL,
 revision bigint NOT NULL CHECK (revision BETWEEN 1 AND 1000),
 nodes jsonb NOT NULL CHECK (jsonb_typeof(nodes)='array'),
 node_count integer NOT NULL CHECK (node_count BETWEEN 0 AND 40),
 created_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,revision),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 CHECK (jsonb_array_length(nodes)=node_count)
);

CREATE TABLE studio_draft_idempotency (
 project_id text NOT NULL,
 workspace_id text NOT NULL,
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
 revision bigint NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(project_id,idempotency_key),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 FOREIGN KEY(project_id,revision) REFERENCES studio_draft_revisions(project_id,revision)
);

-- Revisions are append-only. Restoring a version inserts a new revision.
-- Retention/deletion is a future explicit migration, never a silent overwrite.
CREATE FUNCTION studio_reject_draft_revision_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'draft revisions are immutable' USING ERRCODE='55000';
END;
$$;
CREATE TRIGGER studio_draft_revisions_immutable
 BEFORE UPDATE OR DELETE ON studio_draft_revisions
 FOR EACH ROW EXECUTE FUNCTION studio_reject_draft_revision_mutation();
