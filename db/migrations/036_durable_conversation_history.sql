ALTER TABLE studio_conversations DROP CONSTRAINT studio_conversations_node_count_check;
ALTER TABLE studio_conversations ADD CONSTRAINT studio_conversations_node_count_check CHECK(node_count BETWEEN 0 AND 32768);

-- Metadata only. A pointer describes strictly earlier messages: creation of
-- an assistant precedes its terminal decision, so never predict that decision.
CREATE TABLE studio_chat_ancestry (
 message_id text PRIMARY KEY REFERENCES studio_chat_messages(id),
 depth integer NOT NULL CHECK(depth BETWEEN 1 AND 16384),
 parent_id text REFERENCES studio_chat_ancestry(message_id),
 block_id text REFERENCES studio_chat_ancestry(message_id),
 eligible_id text REFERENCES studio_chat_ancestry(message_id),
 eligible_bytes integer NOT NULL CHECK(eligible_bytes BETWEEN 0 AND 147072),
 CHECK((eligible_id IS NULL)=(eligible_bytes=0)),
 CHECK((parent_id IS NULL)=(depth=1))
);
CREATE FUNCTION studio_chat_released_output(doc jsonb) RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT CASE WHEN doc->'moderation' IS NULL OR doc->'moderation'='null'::jsonb OR
  (doc->'moderation'->>'state'='ALLOWED' AND doc->>'status'='COMPLETED')
  THEN coalesce(doc->>'output','') ELSE '' END
$$;
CREATE FUNCTION studio_index_chat_ancestor(m studio_chat_messages) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
 p studio_chat_ancestry%ROWTYPE;
 parent_role text;
 parent_status text;
 allowed_bytes integer;
BEGIN
 IF m.parent_id IS NULL THEN
  IF m.role<>'user' THEN RAISE EXCEPTION 'invalid conversation root' USING ERRCODE='23514'; END IF;
  INSERT INTO studio_chat_ancestry(message_id,depth,eligible_bytes) VALUES(m.id,1,0);
  RETURN;
 END IF;
 SELECT a.* INTO STRICT p FROM studio_chat_ancestry a WHERE a.message_id=m.parent_id;
 SELECT role INTO STRICT parent_role FROM studio_chat_messages WHERE id=m.parent_id;
 IF parent_role=m.role THEN RAISE EXCEPTION 'invalid conversation roles' USING ERRCODE='23514'; END IF;
 IF m.role='user' THEN
  SELECT r.document->>'status', CASE WHEN studio_chat_released_output(r.document)<>''
    THEN octet_length(studio_chat_released_output(r.document))+octet_length(u.content) ELSE 0 END
  INTO STRICT parent_status,allowed_bytes FROM studio_chat_messages a
  JOIN studio_chat_messages u ON u.id=a.parent_id AND u.role='user'
  JOIN studio_runs r ON r.id=a.run_id AND r.workspace_id=a.workspace_id AND r.project_id=a.project_id AND r.conversation_id=a.conversation_id
  WHERE a.id=m.parent_id;
  IF parent_status IS NULL OR parent_status NOT IN ('COMPLETED','FAILED','CANCELLED','INTERRUPTED','SUCCEEDED','PARTIAL') THEN
   RAISE EXCEPTION 'conversation ancestor is not terminal' USING ERRCODE='23514';
  END IF;
  IF allowed_bytes>0 THEN p.eligible_id:=p.message_id; p.eligible_bytes:=allowed_bytes; END IF;
 END IF;
 INSERT INTO studio_chat_ancestry(message_id,depth,parent_id,block_id,eligible_id,eligible_bytes)
 VALUES(m.id,p.depth+1,m.parent_id,CASE WHEN (p.depth-1)%128=0 THEN p.message_id ELSE p.block_id END,p.eligible_id,p.eligible_bytes);
END $$;

-- Old accepted trees contain at most 128 levels. Refuse disconnected or
-- malformed rows; leave the entire format migration uncommitted on mismatch.
DO $$
DECLARE msg studio_chat_messages%ROWTYPE;
BEGIN
 FOR msg IN WITH RECURSIVE tree(id,conversation_id,depth) AS (
  SELECT id,conversation_id,1 FROM studio_chat_messages WHERE parent_id IS NULL
  UNION ALL SELECT child.id,tree.conversation_id,tree.depth+1 FROM tree JOIN studio_chat_messages child ON child.parent_id=tree.id AND child.conversation_id=tree.conversation_id
   WHERE tree.depth<128
 ) SELECT m.* FROM tree JOIN studio_chat_messages m ON m.id=tree.id ORDER BY tree.depth
 LOOP PERFORM studio_index_chat_ancestor(msg); END LOOP;
 IF EXISTS(SELECT 1 FROM studio_chat_messages m LEFT JOIN studio_chat_ancestry a ON a.message_id=m.id WHERE a.message_id IS NULL) THEN
  RAISE EXCEPTION 'incomplete conversation ancestry backfill' USING ERRCODE='23514';
 END IF;
END $$;
CREATE FUNCTION studio_insert_chat_ancestry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM studio_index_chat_ancestor(NEW::studio_chat_messages); RETURN NEW; END $$;
CREATE TRIGGER studio_chat_ancestry_on_insert AFTER INSERT ON studio_chat_messages
 FOR EACH ROW EXECUTE FUNCTION studio_insert_chat_ancestry();
CREATE TRIGGER studio_chat_ancestry_immutable BEFORE UPDATE OR DELETE ON studio_chat_ancestry
 FOR EACH ROW EXECUTE FUNCTION studio_guard_chat_immutable();

-- For an untrusted inclusive cursor use 128-level block jumps until only
-- the last partial block remains. No message or run body participates.
CREATE FUNCTION studio_chat_is_ancestor(leaf_id text, candidate_id text) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 WITH RECURSIVE walk(id,depth,parent_id,block_id,goal_depth) AS (
  SELECT a.message_id,a.depth,a.parent_id,a.block_id,target.depth FROM studio_chat_ancestry a
  JOIN studio_chat_ancestry target ON target.message_id=candidate_id WHERE a.message_id=leaf_id
  UNION ALL SELECT a.message_id,a.depth,a.parent_id,a.block_id,w.goal_depth FROM walk w
  JOIN studio_chat_ancestry a ON a.message_id=CASE WHEN ((w.depth-2)/128)*128+1>=w.goal_depth THEN w.block_id ELSE w.parent_id END
  WHERE w.depth>w.goal_depth AND a.depth<w.depth
 ) SELECT EXISTS(SELECT 1 FROM walk WHERE id=candidate_id)
$$;
