CREATE TABLE studio_video_timelines (
 id text PRIMARY KEY CHECK(id~'^vtl_[a-f0-9]{32}$'),
 workspace_id text NOT NULL, project_id text NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,project_id,workspace_id), FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_video_timelines_project ON studio_video_timelines(workspace_id,project_id,created_at DESC,id DESC);
CREATE TABLE studio_video_timeline_versions (
 timeline_id text NOT NULL, version bigint NOT NULL CHECK(version BETWEEN 1 AND 128),
 workspace_id text NOT NULL,project_id text NOT NULL,created_by text NOT NULL REFERENCES studio_users(id),
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),clip_count integer NOT NULL CHECK(clip_count BETWEEN 1 AND 8),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(timeline_id,version),UNIQUE(timeline_id,version,project_id,workspace_id),
 FOREIGN KEY(timeline_id,project_id,workspace_id) REFERENCES studio_video_timelines(id,project_id,workspace_id)
);
ALTER TABLE studio_video_timelines ADD CONSTRAINT studio_video_timeline_head_revision FOREIGN KEY(id,version)
 REFERENCES studio_video_timeline_versions(timeline_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE studio_video_timeline_clips (
 timeline_id text NOT NULL,version bigint NOT NULL,project_id text NOT NULL,workspace_id text NOT NULL,
 position integer NOT NULL CHECK(position BETWEEN 0 AND 7),clip_id text NOT NULL CHECK(clip_id~'^[A-Za-z0-9_-]{1,48}$'),
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 in_ms bigint NOT NULL CHECK(in_ms>=0),out_ms bigint NOT NULL CHECK(out_ms>in_ms AND out_ms<=300000 AND out_ms-in_ms<=30000),
 asset_id text NOT NULL,asset_revision bigint NOT NULL CHECK(asset_revision>0),
 content_version text NOT NULL CHECK(content_version~'^[a-f0-9]{64}$'),
 asset_info jsonb NOT NULL CHECK(jsonb_typeof(asset_info)='object' AND asset_info ?& ARRAY['id','sha256','content_type','byte_size']
   AND asset_info->>'sha256' IS NOT DISTINCT FROM content_version AND asset_info->>'content_type' IS NOT DISTINCT FROM 'video/mp4'
   AND jsonb_typeof(asset_info->'byte_size')='number' AND (asset_info->>'byte_size')::bigint BETWEEN 1 AND 33554432
   AND octet_length(asset_info::text)<=65536),
 PRIMARY KEY(timeline_id,version,position),UNIQUE(timeline_id,version,clip_id),
 FOREIGN KEY(timeline_id,version,project_id,workspace_id) REFERENCES studio_video_timeline_versions(timeline_id,version,project_id,workspace_id),
 FOREIGN KEY(asset_id,project_id,workspace_id) REFERENCES studio_library_entries(id,project_id,workspace_id),
 FOREIGN KEY(asset_id,asset_revision) REFERENCES studio_library_revisions(asset_id,revision)
);
CREATE TABLE studio_video_timeline_keys (
 workspace_id text NOT NULL,project_id text NOT NULL,actor_id text NOT NULL REFERENCES studio_users(id),
 key_hash bytea NOT NULL CHECK(octet_length(key_hash)=32),request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 timeline_id text NOT NULL,version bigint NOT NULL,PRIMARY KEY(workspace_id,project_id,actor_id,key_hash),
 FOREIGN KEY(timeline_id,version,project_id,workspace_id) REFERENCES studio_video_timeline_versions(timeline_id,version,project_id,workspace_id)
);
CREATE FUNCTION studio_video_timeline_head_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.version<>1 THEN RAISE EXCEPTION 'first timeline version must be one'; END IF;
 ELSIF TG_OP='DELETE' OR (NEW.id,NEW.workspace_id,NEW.project_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.workspace_id,OLD.project_id,OLD.created_at)
   OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'timeline must advance exactly one version';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_video_timeline_head_guard BEFORE INSERT OR UPDATE OR DELETE ON studio_video_timelines FOR EACH ROW EXECUTE FUNCTION studio_video_timeline_head_guard();
CREATE TRIGGER studio_video_timeline_version_immutable BEFORE UPDATE OR DELETE ON studio_video_timeline_versions FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_video_timeline_clip_immutable BEFORE UPDATE OR DELETE ON studio_video_timeline_clips FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();
CREATE TRIGGER studio_video_timeline_key_immutable BEFORE UPDATE OR DELETE ON studio_video_timeline_keys FOR EACH ROW EXECUTE FUNCTION studio_operations_no_mutation();

-- Validate the complete, ordered snapshot at commit. A later append or a
-- version with missing positions cannot alter what a previously issued key reads.
CREATE FUNCTION studio_video_timeline_validate_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE intended integer; actual integer; last_position integer; duration bigint; source_bytes bigint; empty_frames integer;
BEGIN
 SELECT clip_count INTO STRICT intended FROM studio_video_timeline_versions
  WHERE timeline_id=NEW.timeline_id AND version=NEW.version;
 SELECT count(*),max(position),sum(length_ms),sum(byte_size),
  count(*) FILTER(WHERE (ended_ms*24+500)/1000=((ended_ms-length_ms)*24+500)/1000)
 INTO actual,last_position,duration,source_bytes,empty_frames
 FROM (SELECT position,out_ms-in_ms AS length_ms,(asset_info->>'byte_size')::bigint AS byte_size,
       (sum(out_ms-in_ms) OVER (ORDER BY position))::bigint AS ended_ms
       FROM studio_video_timeline_clips WHERE timeline_id=NEW.timeline_id AND version=NEW.version) ordered_clips;
 IF actual<>intended OR last_position<>intended-1 OR duration>30000 OR source_bytes>134217728 OR empty_frames<>0 THEN
  RAISE EXCEPTION 'invalid timeline snapshot';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER studio_video_timeline_version_complete AFTER INSERT ON studio_video_timeline_versions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_video_timeline_validate_snapshot();
CREATE CONSTRAINT TRIGGER studio_video_timeline_clips_complete AFTER INSERT ON studio_video_timeline_clips
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION studio_video_timeline_validate_snapshot();
