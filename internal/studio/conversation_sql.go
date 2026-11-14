package studio

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

type chatQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const conversationColumns = `c.id,c.workspace_id,c.project_id,c.title,c.version,c.head_message_id,c.node_count,c.created_at,c.updated_at,(SELECT r.id FROM studio_runs r WHERE r.conversation_id=c.id AND r.document->>'status' NOT IN ('COMPLETED','FAILED','CANCELLED','INTERRUPTED') LIMIT 1)`

func scanConversation(row pgx.Row) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.ProjectID, &c.Title, &c.Version, &c.HeadMessageID, &c.NodeCount, &c.CreatedAt, &c.UpdatedAt, &c.ActiveRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conversationNotFound()
	}
	return c, err
}
func readConversationSQL(ctx context.Context, q chatQueryer, workspaceID, projectID, id string, lock bool) (Conversation, error) {
	sql := "SELECT " + conversationColumns + " FROM studio_conversations c WHERE c.id=$1 AND c.workspace_id=$2 AND c.project_id=$3"
	if lock {
		sql += " FOR UPDATE OF c"
	}
	return scanConversation(q.QueryRow(ctx, sql, id, workspaceID, projectID))
}

const chatNodeColumns = `id,conversation_id,workspace_id,project_id,parent_id,role,content,run_id,edited_from_id,created_at`

func scanChatNode(row pgx.Row) (chatNode, error) {
	var n chatNode
	err := row.Scan(&n.ID, &n.ConversationID, &n.WorkspaceID, &n.ProjectID, &n.ParentID, &n.Role, &n.Content, &n.RunID, &n.EditedFromID, &n.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conversationNotFound()
	}
	n.UpdatedAt = n.CreatedAt
	n.Status = "COMPLETED"
	if n.Role == "assistant" {
		n.Status = "QUEUED"
	}
	return n, err
}
func readChatNodeSQL(ctx context.Context, q chatQueryer, workspaceID, projectID, conversationID, id string) (chatNode, error) {
	return scanChatNode(q.QueryRow(ctx, "SELECT "+chatNodeColumns+" FROM studio_chat_messages WHERE id=$1 AND conversation_id=$2 AND workspace_id=$3 AND project_id=$4", id, conversationID, workspaceID, projectID))
}
func readChatPathSQL(ctx context.Context, q chatQueryer, workspaceID, projectID, conversationID, leaf string) ([]chatNode, error) {
	result := []chatNode{}
	if leaf == "" {
		return result, nil
	}
	rows, err := q.Query(ctx, `WITH RECURSIVE ancestry AS (
 SELECT m.*,1 AS depth FROM studio_chat_messages m WHERE m.id=$1 AND m.conversation_id=$2 AND m.workspace_id=$3 AND m.project_id=$4
 UNION ALL SELECT m.*,p.depth+1 FROM studio_chat_messages m JOIN ancestry p ON m.id=p.parent_id AND m.conversation_id=p.conversation_id WHERE p.depth<129
 ) SELECT `+chatNodeColumns+` FROM ancestry ORDER BY depth`, leaf, conversationID, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		n, e := scanChatNode(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, n)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, conversationNotFound()
	}
	if len(result) > conversationPathLimit {
		return nil, failure(422, "INVALID_CHAT_PARENT", "消息路径超过上限")
	}
	return result, nil
}
func readChatRunSQL(ctx context.Context, q chatQueryer, id, workspaceID, projectID string) (Run, error) {
	var data []byte
	var run Run
	err := q.QueryRow(ctx, "SELECT document FROM studio_runs WHERE id=$1 AND workspace_id=$2 AND project_id=$3", id, workspaceID, projectID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return run, conversationNotFound()
	}
	if err == nil {
		err = json.Unmarshal(data, &run)
	}
	return run, err
}
func insertChatNodeSQL(ctx context.Context, tx pgx.Tx, n chatNode) error {
	_, err := tx.Exec(ctx, "INSERT INTO studio_chat_messages("+chatNodeColumns+") VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", n.ID, n.ConversationID, n.WorkspaceID, n.ProjectID, n.ParentID, n.Role, n.Content, n.RunID, n.EditedFromID, n.CreatedAt)
	return err
}

func readChatRunMapSQL(ctx context.Context, q chatQueryer, workspaceID, projectID string, nodes []chatNode) (map[string]Run, error) {
	result := make(map[string]Run)
	ids := []string{}
	for _, n := range nodes {
		if n.RunID != nil {
			ids = append(ids, *n.RunID)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := q.Query(ctx, "SELECT document FROM studio_runs WHERE id=ANY($1::text[]) AND workspace_id=$2 AND project_id=$3", ids, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var blob []byte
		var r Run
		if err = rows.Scan(&blob); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(blob, &r); err != nil {
			return nil, err
		}
		result[r.ID] = r
	}
	return result, rows.Err()
}

func (s *postgresStore) loadChatExecution(runID string) (*chatExecution, error) {
	var result *chatExecution
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		var data []byte
		if err := conn.QueryRow(ctx, "SELECT snapshot FROM studio_chat_contexts WHERE run_id=$1", runID).Scan(&data); err != nil {
			return err
		}
		return json.Unmarshal(data, &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, conversationNotFound()
	}
	return result, err
}
