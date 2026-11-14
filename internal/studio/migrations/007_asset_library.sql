CREATE TABLE studio_storage_identity (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 storage_id uuid NOT NULL
);
INSERT INTO studio_storage_identity(singleton,storage_id) VALUES(true,gen_random_uuid());

CREATE TABLE studio_library_entries (
 id text PRIMARY KEY,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 source text NOT NULL CHECK(source IN ('reference','generated')),
 reference_id text,
 task_id text,
 artifact_id text,
 provider_mode text CHECK(provider_mode IN ('demo','live')),
 kind text NOT NULL CHECK(kind IN ('image','video','poster')),
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 tags text[] NOT NULL DEFAULT '{}' CHECK(cardinality(tags)<=10),
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','trashed')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 1000),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 deleted_at timestamptz,
 content_type text,
 byte_size bigint CHECK(byte_size IS NULL OR byte_size>0),
 has_thumbnail boolean NOT NULL DEFAULT false,
 original_url text NOT NULL DEFAULT '',
 storage_info jsonb,
 UNIQUE(id,project_id,workspace_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id),
 FOREIGN KEY(reference_id,project_id,workspace_id) REFERENCES studio_references(id,project_id,workspace_id),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id),
 CHECK((source='reference' AND reference_id IS NOT NULL AND task_id IS NULL AND artifact_id IS NULL AND provider_mode IS NULL) OR (source='generated' AND reference_id IS NULL AND task_id IS NOT NULL AND artifact_id IS NOT NULL AND provider_mode IS NOT NULL)),
 CHECK((state='active' AND deleted_at IS NULL) OR (state='trashed' AND deleted_at IS NOT NULL))
);
CREATE UNIQUE INDEX studio_library_reference_identity ON studio_library_entries(reference_id) WHERE source='reference';
CREATE UNIQUE INDEX studio_library_generated_identity ON studio_library_entries(task_id,artifact_id) WHERE source='generated';
CREATE INDEX studio_library_project_page ON studio_library_entries(workspace_id,project_id,state,created_at DESC,id DESC);
CREATE INDEX studio_library_artifact_access ON studio_library_entries(workspace_id,artifact_id,state) WHERE source='generated';

CREATE TABLE studio_library_revisions (
 asset_id text NOT NULL,
 project_id text NOT NULL,
 workspace_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 1000),
 name text NOT NULL,
 tags text[] NOT NULL,
 state text NOT NULL CHECK(state IN ('active','trashed')),
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(asset_id,revision),
 FOREIGN KEY(asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id)
);
CREATE TABLE studio_library_idempotency (
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 payload_hash bytea NOT NULL CHECK(octet_length(payload_hash)=32),
 asset_id text NOT NULL,
 revision bigint NOT NULL,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,idempotency_key),
 FOREIGN KEY(asset_id,revision) REFERENCES studio_library_revisions(asset_id,revision)
);
CREATE FUNCTION studio_reject_library_revision_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'asset metadata revisions are immutable' USING ERRCODE='55000'; END;
$$;
CREATE TRIGGER studio_library_revisions_immutable BEFORE UPDATE OR DELETE ON studio_library_revisions FOR EACH ROW EXECUTE FUNCTION studio_reject_library_revision_mutation();
CREATE FUNCTION studio_guard_library_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.workspace_id,NEW.project_id,NEW.source,NEW.reference_id,NEW.task_id,NEW.artifact_id,NEW.provider_mode,NEW.kind,NEW.created_at,NEW.original_url) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.project_id,OLD.source,OLD.reference_id,OLD.task_id,OLD.artifact_id,OLD.provider_mode,OLD.kind,OLD.created_at,OLD.original_url) THEN
  RAISE EXCEPTION 'asset source identity is immutable' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER studio_library_source_immutable BEFORE UPDATE ON studio_library_entries FOR EACH ROW EXECUTE FUNCTION studio_guard_library_source();

-- Backfill metadata only. No network or file access, no fabricated user audit.
INSERT INTO studio_library_entries(id,workspace_id,project_id,source,reference_id,kind,name,created_at,updated_at,content_type,byte_size,has_thumbnail)
 SELECT 'lib_'||encode(sha256(convert_to('reference:'||id,'UTF8')),'hex'),workspace_id,project_id,'reference',id,'image',coalesce(nullif(left(name,120),''),'参考图片'),created_at,created_at,blob->>'content_type',(blob->>'byte_size')::bigint,blob->'thumbnail' IS NOT NULL AND blob->'thumbnail'<>'null'::jsonb
 FROM studio_references;
INSERT INTO studio_library_entries(id,workspace_id,project_id,source,task_id,artifact_id,provider_mode,kind,name,created_at,updated_at,has_thumbnail,original_url)
 SELECT 'lib_'||encode(sha256(convert_to('generated:'||t.id||':'||(art->>'id'),'UTF8')),'hex'),t.workspace_id,t.project_id,'generated',t.id,art->>'id',coalesce(nullif(t.document->>'provider_mode',''),'demo'),
 CASE WHEN art->>'kind' IN ('image','video','poster') THEN art->>'kind' ELSE 'image' END,
 left(coalesce(nullif(art->>'title',''),'生成作品'),120),coalesce((t.document->>'created_at')::timestamptz,t.updated_at),t.updated_at,coalesce((art->>'url') LIKE '/demo-assets/%',false),coalesce(art->>'url','')
 FROM studio_tasks t CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(t.document->'artifacts')='array' THEN t.document->'artifacts' ELSE '[]'::jsonb END) art
 WHERE coalesce(art->>'id','')<>'' ON CONFLICT DO NOTHING;
INSERT INTO studio_library_revisions(asset_id,project_id,workspace_id,revision,name,tags,state,updated_at)
 SELECT id,project_id,workspace_id,revision,name,tags,state,updated_at FROM studio_library_entries;

ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_action_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_action_check CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.reconcile_requested','draft.saved','reference.uploaded','asset.updated','asset.trashed','asset.restored'));
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_resource_type_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_resource_type_check CHECK(resource_type IN ('project','run','task','draft','reference','asset'));
