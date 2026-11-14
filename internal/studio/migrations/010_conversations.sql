CREATE TABLE studio_conversations (
 id text PRIMARY KEY,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 title text NOT NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>=1 AND version<=9007199254740991),
 head_message_id text,
 node_count integer NOT NULL DEFAULT 0 CHECK(node_count BETWEEN 0 AND 256),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE(id,workspace_id,project_id),
 FOREIGN KEY(project_id,workspace_id) REFERENCES studio_projects(id,workspace_id)
);
CREATE INDEX studio_conversations_page ON studio_conversations(workspace_id,project_id,created_at DESC,id DESC);
ALTER TABLE studio_runs ADD COLUMN conversation_id text;
ALTER TABLE studio_runs ADD CONSTRAINT studio_run_scope_identity UNIQUE(id,workspace_id,project_id);
ALTER TABLE studio_runs ADD CONSTRAINT studio_run_conversation_scope FOREIGN KEY(conversation_id,workspace_id,project_id) REFERENCES studio_conversations(id,workspace_id,project_id);
CREATE UNIQUE INDEX studio_conversation_single_active ON studio_runs(conversation_id) WHERE conversation_id IS NOT NULL AND document->>'status' NOT IN ('COMPLETED','FAILED','CANCELLED','INTERRUPTED');
CREATE INDEX studio_conversation_runs ON studio_runs(conversation_id,id);
CREATE TABLE studio_chat_messages (
 id text PRIMARY KEY,
 conversation_id text NOT NULL,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 parent_id text,
 role text NOT NULL CHECK(role IN ('user','assistant')),
 content text NOT NULL DEFAULT '',
 run_id text,
 edited_from_id text,
 created_at timestamptz NOT NULL,
 CHECK((role='user' AND run_id IS NULL AND octet_length(content) BETWEEN 1 AND 16000) OR (role='assistant' AND run_id IS NOT NULL AND content='')),
 UNIQUE(id,conversation_id,workspace_id,project_id),
 UNIQUE(run_id),
 FOREIGN KEY(conversation_id,workspace_id,project_id) REFERENCES studio_conversations(id,workspace_id,project_id),
 FOREIGN KEY(parent_id,conversation_id,workspace_id,project_id) REFERENCES studio_chat_messages(id,conversation_id,workspace_id,project_id),
 FOREIGN KEY(edited_from_id,conversation_id,workspace_id,project_id) REFERENCES studio_chat_messages(id,conversation_id,workspace_id,project_id),
 FOREIGN KEY(run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id)
);
CREATE INDEX studio_chat_message_children ON studio_chat_messages(conversation_id,parent_id);
CREATE INDEX studio_chat_message_page ON studio_chat_messages(conversation_id,created_at DESC,id DESC);
ALTER TABLE studio_conversations ADD CONSTRAINT studio_conversation_head_scope FOREIGN KEY(head_message_id,id,workspace_id,project_id) REFERENCES studio_chat_messages(id,conversation_id,workspace_id,project_id);
CREATE TABLE studio_chat_contexts (
 run_id text PRIMARY KEY REFERENCES studio_runs(id),
 conversation_id text NOT NULL,
 workspace_id text NOT NULL,
 project_id text NOT NULL,
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=2097152),
 FOREIGN KEY(run_id,workspace_id,project_id) REFERENCES studio_runs(id,workspace_id,project_id),
 FOREIGN KEY(conversation_id,workspace_id,project_id) REFERENCES studio_conversations(id,workspace_id,project_id)
);
CREATE FUNCTION studio_guard_chat_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'chat node and execution context are immutable' USING ERRCODE='55000'; END $$;
CREATE TRIGGER studio_chat_nodes_immutable BEFORE UPDATE OR DELETE ON studio_chat_messages FOR EACH ROW EXECUTE FUNCTION studio_guard_chat_immutable();
CREATE TRIGGER studio_chat_context_immutable BEFORE UPDATE OR DELETE ON studio_chat_contexts FOR EACH ROW EXECUTE FUNCTION studio_guard_chat_immutable();
ALTER TABLE studio_idempotency DROP CONSTRAINT studio_idempotency_resource_type_check;
ALTER TABLE studio_idempotency ADD CONSTRAINT studio_idempotency_resource_type_check CHECK(resource_type IN ('run','task','conversation'));
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_action_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_action_check CHECK(action IN ('project.created','run.accepted','task.accepted','run.cancelled','task.cancelled','task.reconcile_requested','draft.saved','reference.uploaded','asset.updated','asset.trashed','asset.restored','conversation.created','conversation.turn_created'));
ALTER TABLE studio_audit_events DROP CONSTRAINT studio_audit_events_resource_type_check;
ALTER TABLE studio_audit_events ADD CONSTRAINT studio_audit_events_resource_type_check CHECK(resource_type IN ('project','run','task','draft','reference','asset','conversation'));
ALTER TABLE studio_outbox_events DROP CONSTRAINT studio_outbox_events_resource_type_check;
ALTER TABLE studio_outbox_events ADD CONSTRAINT studio_outbox_events_resource_type_check CHECK(resource_type IN ('task','run','project','draft','reference','asset','conversation'));
