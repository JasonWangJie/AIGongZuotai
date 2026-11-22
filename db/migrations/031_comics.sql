-- A project row serializes even the first creation of its storyboard.
CREATE TABLE studio_comics (
 project_id text PRIMARY KEY, workspace_id text NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 UNIQUE(project_id,workspace_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE TABLE studio_comic_versions (
 project_id text NOT NULL,workspace_id text NOT NULL,version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 created_by text NOT NULL REFERENCES studio_users(id),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object' AND document ?& ARRAY['title','story','characters','scenes','panels']
  AND char_length(document->>'title') BETWEEN 1 AND 120 AND char_length(document->>'story') BETWEEN 1 AND 4000
  AND jsonb_array_length(document->'characters') BETWEEN 1 AND 16 AND jsonb_array_length(document->'scenes') BETWEEN 1 AND 16
  AND jsonb_array_length(document->'panels') BETWEEN 1 AND 12 AND octet_length(document::text)<=327680),
 PRIMARY KEY(project_id,version),UNIQUE(project_id,workspace_id,version),UNIQUE(project_id,workspace_id,version,created_by),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_comics(project_id,workspace_id)
);
ALTER TABLE studio_comics ADD CONSTRAINT studio_comic_current_version FOREIGN KEY(project_id,version)
 REFERENCES studio_comic_versions(project_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE studio_comic_assets (
 project_id text NOT NULL,workspace_id text NOT NULL,version bigint NOT NULL,panel_id text NOT NULL CHECK(panel_id~'^[A-Za-z0-9_-]{1,48}$'),
 asset_id text NOT NULL,asset_revision bigint NOT NULL,content_version text NOT NULL CHECK(content_version~'^[a-f0-9]{64}$'),
 original jsonb NOT NULL CHECK(jsonb_typeof(original)='object' AND octet_length(original::text)<=65536),
 PRIMARY KEY(project_id,version,panel_id),
 FOREIGN KEY(project_id,workspace_id,version) REFERENCES studio_comic_versions(project_id,workspace_id,version),
 FOREIGN KEY(asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id),
 FOREIGN KEY(asset_id,asset_revision) REFERENCES studio_library_revisions(asset_id,revision)
);
-- A confirm receipt is the author's decision for that exact version. The
-- version/author foreign key also covers the save receipt. Neither can be
-- reassigned, overwritten or repeated under a different key.
CREATE TABLE studio_comic_receipts (
 project_id text NOT NULL,workspace_id text NOT NULL,actor_id text NOT NULL,operation text NOT NULL CHECK(operation IN('save','confirm')),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 version bigint NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(project_id,actor_id,operation,key_hash),UNIQUE(project_id,version,operation),
 FOREIGN KEY(project_id,workspace_id,version,actor_id) REFERENCES studio_comic_versions(project_id,workspace_id,version,created_by)
);
CREATE FUNCTION studio_comic_head_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'first storyboard version must be one'; END IF;
 ELSIF TG_OP='DELETE' OR (NEW.project_id,NEW.workspace_id) IS DISTINCT FROM (OLD.project_id,OLD.workspace_id)
       OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'storyboard must advance exactly one version';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_comic_head_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_comics FOR EACH ROW EXECUTE FUNCTION studio_comic_head_guard();
CREATE TRIGGER studio_comic_version_immutable BEFORE UPDATE OR DELETE ON studio_comic_versions FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_comic_asset_immutable BEFORE UPDATE OR DELETE ON studio_comic_assets FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_comic_receipt_immutable BEFORE UPDATE OR DELETE ON studio_comic_receipts FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
