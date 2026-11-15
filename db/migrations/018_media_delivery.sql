-- Private frozen source descriptors; URLs only exist inside ciphertext.
CREATE TABLE studio_media_deliveries (
 task_id text PRIMARY KEY,
 id text NOT NULL UNIQUE CHECK(id ~ '^delivery_[a-f0-9]{32}$'),
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 format_version integer NOT NULL CHECK(format_version=1),
 state text NOT NULL CHECK(state IN('READY','COMPLETE','PARKED')),
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 key_id text NOT NULL CHECK(key_id ~ '^[a-f0-9]{32}$'),
 manifest_digest text NOT NULL CHECK(manifest_digest ~ '^[a-f0-9]{64}$'),
 handle_digest text NOT NULL CHECK(handle_digest ~ '^[a-f0-9]{64}$'),
 ciphertext bytea NOT NULL CHECK(octet_length(ciphertext) BETWEEN 29 AND 65600),
 item_count integer NOT NULL CHECK(item_count BETWEEN 1 AND 4),
 result_status text NOT NULL CHECK(result_status IN('SUCCEEDED','PARTIAL')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(task_id,project_id,workspace_id) REFERENCES studio_tasks(id,project_id,workspace_id)
);
CREATE TABLE studio_media_delivery_items (
 task_id text NOT NULL REFERENCES studio_media_deliveries(task_id),
 position integer NOT NULL CHECK(position BETWEEN 0 AND 3),
 asset_id text NOT NULL UNIQUE CHECK(asset_id ~ '^asset_[a-f0-9]{64}$'),
 receipt jsonb CHECK(receipt IS NULL OR (jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=16384)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 receipted_at timestamptz,
 PRIMARY KEY(task_id,position),
 CHECK((receipt IS NULL)=(receipted_at IS NULL))
);
CREATE INDEX studio_media_deliveries_ready ON studio_media_deliveries(task_id) WHERE state='READY';
CREATE FUNCTION studio_guard_media_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'media delivery evidence cannot be deleted'; END IF;
 IF (NEW.task_id,NEW.id,NEW.workspace_id,NEW.project_id,NEW.format_version,NEW.key_id,NEW.manifest_digest,NEW.handle_digest,NEW.ciphertext,NEW.item_count,NEW.result_status,NEW.created_at) IS DISTINCT FROM (OLD.task_id,OLD.id,OLD.workspace_id,OLD.project_id,OLD.format_version,OLD.key_id,OLD.manifest_digest,OLD.handle_digest,OLD.ciphertext,OLD.item_count,OLD.result_status,OLD.created_at) THEN RAISE EXCEPTION 'media delivery manifest is immutable'; END IF;
 IF OLD.state<>'READY' OR NEW.version<>OLD.version+1 THEN RAISE EXCEPTION 'media delivery transition invalid'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_media_delivery_guard BEFORE UPDATE OR DELETE ON studio_media_deliveries FOR EACH ROW EXECUTE FUNCTION studio_guard_media_delivery();
CREATE FUNCTION studio_guard_media_delivery_item() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'media delivery receipt cannot be deleted'; END IF;
 IF (NEW.task_id,NEW.position,NEW.asset_id,NEW.created_at) IS DISTINCT FROM (OLD.task_id,OLD.position,OLD.asset_id,OLD.created_at) OR OLD.receipt IS NOT NULL OR NEW.receipt IS NULL OR NEW.receipted_at IS NULL THEN RAISE EXCEPTION 'media delivery receipt is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER studio_media_delivery_item_guard BEFORE UPDATE OR DELETE ON studio_media_delivery_items FOR EACH ROW EXECUTE FUNCTION studio_guard_media_delivery_item();
