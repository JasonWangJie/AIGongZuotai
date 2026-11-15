-- Private execution scheduling projection. Business documents and task leases
-- remain authoritative; no provider request, secret or result URL is copied.
CREATE TABLE studio_lane_schedule (
 lane text NOT NULL CHECK(lane IN ('submit','query','delivery','review')),
 provider_mode text NOT NULL CHECK(provider_mode IN ('demo','live')),
 workspace_cursor text NOT NULL DEFAULT '',
 PRIMARY KEY(lane,provider_mode)
);
INSERT INTO studio_lane_schedule(lane,provider_mode)
 SELECT l,m FROM unnest(ARRAY['submit','query','delivery','review']) l CROSS JOIN unnest(ARRAY['demo','live']) m;
CREATE TABLE studio_workspace_schedule (
 lane text NOT NULL,
 provider_mode text NOT NULL,
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 active_count bigint NOT NULL DEFAULT 0 CHECK(active_count>=0),
 task_cursor text NOT NULL DEFAULT '',
 PRIMARY KEY(lane,provider_mode,workspace_id),
 FOREIGN KEY(lane,provider_mode) REFERENCES studio_lane_schedule(lane,provider_mode)
);
CREATE TABLE studio_task_schedule (
 task_id text PRIMARY KEY REFERENCES studio_tasks(id),
 workspace_id text NOT NULL REFERENCES studio_workspaces(id),
 provider_mode text NOT NULL CHECK(provider_mode IN ('demo','live')),
 lane text NOT NULL CHECK(lane IN ('submit','query','delivery','review')),
 channel_key text NOT NULL CHECK(channel_key ~ '^[a-f0-9]{64}$'),
 eligible_at timestamptz NOT NULL,
 server_not_before timestamptz NOT NULL DEFAULT '1970-01-01T00:00:00Z',
 failure_count integer NOT NULL DEFAULT 0 CHECK(failure_count BETWEEN 0 AND 30),
 unchanged_count integer NOT NULL DEFAULT 0 CHECK(unchanged_count BETWEEN 0 AND 30),
 last_progress integer NOT NULL DEFAULT 0 CHECK(last_progress BETWEEN 0 AND 100),
 active boolean NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 last_observation_id text NOT NULL DEFAULT '',
 FOREIGN KEY(lane,provider_mode,workspace_id) REFERENCES studio_workspace_schedule(lane,provider_mode,workspace_id)
);
CREATE TABLE studio_media_channel_runtime (
 scope text NOT NULL CHECK(scope IN ('gateway','source','storage','review')),
 lane text NOT NULL CHECK(lane IN ('submit','query','delivery','review')),
 channel_key text NOT NULL CHECK(channel_key ~ '^[a-f0-9]{64}$'),
 cooldown_until timestamptz NOT NULL DEFAULT '1970-01-01T00:00:00Z',
 parked boolean NOT NULL DEFAULT false,
 failure_count integer NOT NULL DEFAULT 0 CHECK(failure_count BETWEEN 0 AND 30),
 success_count integer NOT NULL DEFAULT 0 CHECK(success_count BETWEEN 0 AND 8),
 effective_limit integer NOT NULL CHECK(effective_limit BETWEEN 1 AND 32),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 probe_required boolean NOT NULL DEFAULT false,
 probe_id text NOT NULL DEFAULT '',
 probe_until timestamptz,
 PRIMARY KEY(scope,lane,channel_key),
 CHECK((probe_id='' AND probe_until IS NULL) OR (probe_id<>'' AND probe_until IS NOT NULL))
);

-- Synchronise projection and active queue membership in the business TX.
-- Raw test/maintenance inserts also get a projection; the final save hook
-- calls this after private delivery/review stage changes.
CREATE FUNCTION studio_sync_task_schedule(wanted text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE t studio_tasks%ROWTYPE; old studio_task_schedule%ROWTYPE;
 l text; c text; m text; live boolean; k record;
BEGIN
 SELECT * INTO t FROM studio_tasks WHERE id=wanted;
 IF NOT FOUND THEN RETURN; END IF;
 m:=t.document->>'provider_mode';
 IF m NOT IN ('demo','live') THEN RETURN; END IF;
 l:=CASE WHEN EXISTS(SELECT 1 FROM studio_media_review_dispatch WHERE task_id=wanted) THEN 'review'
         WHEN EXISTS(SELECT 1 FROM studio_media_deliveries WHERE task_id=wanted) THEN 'delivery'
         WHEN COALESCE(t.handle->>'ID','')<>'' THEN 'query' ELSE 'submit' END;
 c:=CASE WHEN l='review' THEN (SELECT policy->>'adapter_identity' FROM studio_task_media_moderation WHERE task_id=wanted)
         ELSE COALESCE(NULLIF(t.submission->>'provider_identity',''),NULLIF(t.handle->>'ProviderIdentity','')) END;
 IF c IS NULL OR c !~ '^[a-f0-9]{64}$' THEN c:=repeat(md5('media-legacy-v1:'||m||':'||l),2); END IF;
 live:=COALESCE(t.document->>'status','') NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED');
 SELECT * INTO old FROM studio_task_schedule WHERE task_id=wanted FOR UPDATE;
 -- Workspace locks have a deterministic order when a stage moves queues.
 FOR k IN SELECT DISTINCT x.lane,x.mode,x.ws FROM (VALUES(l,m,t.workspace_id),(old.lane,old.provider_mode,old.workspace_id)) x(lane,mode,ws) WHERE x.lane IS NOT NULL ORDER BY x.lane,x.mode,x.ws LOOP
   INSERT INTO studio_workspace_schedule(lane,provider_mode,workspace_id) VALUES(k.lane,k.mode,k.ws) ON CONFLICT DO NOTHING;
   PERFORM 1 FROM studio_workspace_schedule WHERE lane=k.lane AND provider_mode=k.mode AND workspace_id=k.ws FOR UPDATE;
 END LOOP;
 IF old.task_id IS NOT NULL AND old.active THEN
   UPDATE studio_workspace_schedule SET active_count=active_count-1 WHERE lane=old.lane AND provider_mode=old.provider_mode AND workspace_id=old.workspace_id;
 END IF;
 IF live THEN UPDATE studio_workspace_schedule SET active_count=active_count+1 WHERE lane=l AND provider_mode=m AND workspace_id=t.workspace_id; END IF;
 INSERT INTO studio_task_schedule(task_id,workspace_id,provider_mode,lane,channel_key,eligible_at,last_progress,active)
 VALUES(wanted,t.workspace_id,m,l,c,t.next_poll,LEAST(100,GREATEST(0,COALESCE((t.document->>'progress')::int,0))),live)
 ON CONFLICT(task_id) DO UPDATE SET lane=EXCLUDED.lane,channel_key=CASE WHEN studio_task_schedule.lane=EXCLUDED.lane THEN studio_task_schedule.channel_key ELSE EXCLUDED.channel_key END,
 failure_count=CASE WHEN studio_task_schedule.lane=EXCLUDED.lane THEN studio_task_schedule.failure_count ELSE 0 END,
 unchanged_count=CASE WHEN studio_task_schedule.lane=EXCLUDED.lane THEN studio_task_schedule.unchanged_count ELSE 0 END,
 last_progress=CASE WHEN studio_task_schedule.lane=EXCLUDED.lane THEN studio_task_schedule.last_progress ELSE EXCLUDED.last_progress END,
 server_not_before=CASE WHEN studio_task_schedule.lane=EXCLUDED.lane THEN studio_task_schedule.server_not_before ELSE '1970-01-01T00:00:00Z'::timestamptz END,
 eligible_at=EXCLUDED.eligible_at,active=EXCLUDED.active,version=studio_task_schedule.version+1;
END $$;
CREATE FUNCTION studio_task_schedule_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM studio_sync_task_schedule(NEW.id); RETURN NEW; END $$;
CREATE TRIGGER studio_task_schedule_changed AFTER INSERT OR UPDATE OF document,handle,submission,next_poll ON studio_tasks FOR EACH ROW EXECUTE FUNCTION studio_task_schedule_trigger();
-- Upgrade is set-based, not one PL/pgSQL transaction fragment per historical
-- task. The compact temporary projection contains no prompts or source URLs.
CREATE TEMP TABLE studio_media_schedule_seed ON COMMIT DROP AS
WITH stage AS (
 SELECT t.id,t.workspace_id,t.document->>'provider_mode' AS provider_mode,t.next_poll,
 LEAST(100,GREATEST(0,COALESCE((t.document->>'progress')::int,0))) AS progress,
 COALESCE(t.document->>'status','') NOT IN ('SUCCEEDED','PARTIAL','FAILED','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED') AS active,
 CASE WHEN r.task_id IS NOT NULL THEN 'review' WHEN d.task_id IS NOT NULL THEN 'delivery' WHEN COALESCE(t.handle->>'ID','')<>'' THEN 'query' ELSE 'submit' END AS lane,
 CASE WHEN r.task_id IS NOT NULL THEN p.policy->>'adapter_identity' ELSE COALESCE(NULLIF(t.submission->>'provider_identity',''),NULLIF(t.handle->>'ProviderIdentity','')) END AS identity
 FROM studio_tasks t LEFT JOIN studio_media_review_dispatch r ON r.task_id=t.id LEFT JOIN studio_media_deliveries d ON d.task_id=t.id LEFT JOIN studio_task_media_moderation p ON p.task_id=t.id
 WHERE t.document->>'provider_mode' IN ('demo','live')
)
SELECT id,workspace_id,provider_mode,next_poll,progress,active,lane,
 CASE WHEN identity ~ '^[a-f0-9]{64}$' THEN identity ELSE repeat(md5('media-legacy-v1:'||provider_mode||':'||lane),2) END AS channel_key FROM stage;
INSERT INTO studio_workspace_schedule(lane,provider_mode,workspace_id,active_count)
 SELECT lane,provider_mode,workspace_id,count(*) FILTER(WHERE active) FROM studio_media_schedule_seed GROUP BY lane,provider_mode,workspace_id;
INSERT INTO studio_task_schedule(task_id,workspace_id,provider_mode,lane,channel_key,eligible_at,last_progress,active)
 SELECT id,workspace_id,provider_mode,lane,channel_key,next_poll,progress,active FROM studio_media_schedule_seed;
-- Bulk-build secondary indexes after the upgrade projection is populated.
-- Explicit lane SQL makes these predicates usable by generic prepared plans.
CREATE INDEX studio_workspace_schedule_active ON studio_workspace_schedule(lane,provider_mode,workspace_id) WHERE active_count>0;
CREATE INDEX studio_task_schedule_submit ON studio_task_schedule(provider_mode,workspace_id,task_id) WHERE active AND lane='submit';
CREATE INDEX studio_task_schedule_query ON studio_task_schedule(provider_mode,workspace_id,task_id) WHERE active AND lane='query';
CREATE INDEX studio_task_schedule_delivery ON studio_task_schedule(provider_mode,workspace_id,task_id) WHERE active AND lane='delivery';
CREATE INDEX studio_task_schedule_review ON studio_task_schedule(provider_mode,workspace_id,task_id) WHERE active AND lane='review';
