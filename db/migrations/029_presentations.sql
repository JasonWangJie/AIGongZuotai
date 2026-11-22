-- Presentation content is immutable per project version. Visibility of an
-- embedded image is rechecked against the current asset/catalog and decision.
CREATE TABLE studio_presentations (
 id text PRIMARY KEY CHECK(id~'^pres_[a-f0-9]{32}$'),
 workspace_id text NOT NULL,project_id text NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 slide_count integer NOT NULL CHECK(slide_count BETWEEN 1 AND 30),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,project_id,workspace_id),FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_presentations_project ON studio_presentations(workspace_id,project_id,created_at DESC,id DESC);
CREATE TABLE studio_presentation_versions (
 presentation_id text NOT NULL,workspace_id text NOT NULL,project_id text NOT NULL,version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 created_by text NOT NULL REFERENCES studio_users(id),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object' AND jsonb_typeof(document->'slides')='array' AND jsonb_array_length(document->'slides') BETWEEN 1 AND 30 AND octet_length(document::text)<=327680),
 PRIMARY KEY(presentation_id,version),UNIQUE(presentation_id,version,project_id,workspace_id),
 FOREIGN KEY(presentation_id,project_id,workspace_id) REFERENCES studio_presentations(id,project_id,workspace_id)
);
ALTER TABLE studio_presentations ADD CONSTRAINT studio_presentations_current_version
 FOREIGN KEY(id,version) REFERENCES studio_presentation_versions(presentation_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE studio_presentation_assets (
 presentation_id text NOT NULL,version bigint NOT NULL,project_id text NOT NULL,workspace_id text NOT NULL,slide_id text NOT NULL,
 asset_id text NOT NULL,asset_revision bigint NOT NULL,content_version text NOT NULL CHECK(content_version~'^[a-f0-9]{64}$'),
 PRIMARY KEY(presentation_id,version,slide_id),
 FOREIGN KEY(presentation_id,version,project_id,workspace_id) REFERENCES studio_presentation_versions(presentation_id,version,project_id,workspace_id),
 FOREIGN KEY(asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id),
 FOREIGN KEY(asset_id,asset_revision) REFERENCES studio_library_revisions(asset_id,revision)
);
CREATE TABLE studio_presentation_keys (
 workspace_id text NOT NULL,project_id text NOT NULL,actor_id text NOT NULL REFERENCES studio_users(id),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 presentation_id text NOT NULL,version bigint NOT NULL,PRIMARY KEY(workspace_id,project_id,actor_id,key_hash),
 FOREIGN KEY(presentation_id,version,project_id,workspace_id) REFERENCES studio_presentation_versions(presentation_id,version,project_id,workspace_id)
);
CREATE TRIGGER studio_presentation_version_immutable BEFORE UPDATE OR DELETE ON studio_presentation_versions FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_presentation_asset_immutable BEFORE UPDATE OR DELETE ON studio_presentation_assets FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_presentation_key_immutable BEFORE UPDATE OR DELETE ON studio_presentation_keys FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE FUNCTION studio_presentation_header_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'first presentation version must be one'; END IF;
 ELSIF TG_OP='DELETE' OR (NEW.id,NEW.workspace_id,NEW.project_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.project_id,OLD.created_at) OR NEW.version<>OLD.version+1 THEN
  RAISE EXCEPTION 'presentation must advance exactly one version';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_presentation_header_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_presentations FOR EACH ROW EXECUTE FUNCTION studio_presentation_header_guard();
