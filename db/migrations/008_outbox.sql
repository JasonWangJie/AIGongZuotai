CREATE TABLE studio_event_heads (
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 last_seq bigint NOT NULL DEFAULT 0 CHECK(last_seq>=0 AND last_seq<=9007199254740991),
 floor_seq bigint NOT NULL DEFAULT 0 CHECK(floor_seq>=0 AND floor_seq<=last_seq),
 PRIMARY KEY(workspace_id,project_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE TABLE studio_outbox_limits (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 pending bigint NOT NULL DEFAULT 0 CHECK(pending>=0 AND pending<=10000),
 retained bigint NOT NULL DEFAULT 0 CHECK(retained>=0)
);
INSERT INTO studio_outbox_limits(singleton) VALUES(true);
CREATE TABLE studio_outbox_events (
 queue_order bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id text NOT NULL UNIQUE,
 event_key text NOT NULL UNIQUE,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 project_seq bigint NOT NULL CHECK(project_seq>0),
 kind text NOT NULL CHECK(kind IN ('task.updated','run.updated','audit.created')),
 resource_type text NOT NULL CHECK(resource_type IN ('task','run','project','draft','reference','asset')),
 resource_id text NOT NULL,
 resource_version bigint NOT NULL CHECK(resource_version>=0),
 status text NOT NULL DEFAULT '',
 action text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 projected_at timestamptz,
 UNIQUE(workspace_id,project_id,project_seq),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_outbox_pending ON studio_outbox_events(queue_order) WHERE projected_at IS NULL;
CREATE INDEX studio_outbox_retained ON studio_outbox_events(queue_order) WHERE projected_at IS NOT NULL;
CREATE TABLE studio_project_event_feed (
 event_id text PRIMARY KEY REFERENCES studio_outbox_events(event_id) ON DELETE CASCADE,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 project_seq bigint NOT NULL,
 UNIQUE(workspace_id,project_id,project_seq),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE TABLE studio_event_consumers (
 consumer text NOT NULL CHECK(consumer='project-feed-v1'),
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 last_seq bigint NOT NULL DEFAULT 0 CHECK(last_seq>=0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(consumer,workspace_id,project_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
