package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
)

type chatMessagePageBuilder struct {
	conversationMessagePage
	cursor conversationCursor
	bytes  int
}

func newChatMessagePage(r *http.Request, c Conversation) (*chatMessagePageBuilder, error) {
	leaf := r.URL.Query().Get("leaf_id")
	if leaf == "" {
		leaf = stringID(c.HeadMessageID)
	}
	if leaf != "" && !validChatID(leaf, "msg") {
		return nil, conversationNotFound()
	}
	cursor, err := parseChatCursor(r.URL.Query().Get("before"), chatCursorScope(r, "messages", c.ID, leaf))
	if err != nil {
		return nil, err
	}
	page := conversationMessagePage{Items: []MessageNode{}, ConversationVersion: c.Version}
	if leaf != "" {
		page.LeafMessageID = &leaf
	}
	// Preserve the v1 response envelope allowance, including the cursor.
	return &chatMessagePageBuilder{conversationMessagePage: page, cursor: cursor, bytes: 4096}, nil
}

func invalidChatPageAncestor() error {
	return failure(400, "INVALID_CHAT_CURSOR", "分页祖先不属于选中分支")
}

func (p *chatMessagePageBuilder) add(node MessageNode) (bool, error) {
	data, err := json.Marshal(node)
	if err != nil {
		return false, err
	}
	if p.bytes+len(data)+1 > 1<<20 {
		if len(p.Items) == 0 {
			return false, failure(413, "CHAT_PAGE_TOO_LARGE", "单条消息超过读取预算")
		}
		return false, nil
	}
	p.bytes += len(data) + 1
	p.Items = append(p.Items, node)
	return true, nil
}

func (p *chatMessagePageBuilder) finish(nextID string) conversationMessagePage {
	if nextID != "" {
		p.HasMore = true
		p.cursor.BeforeID = nextID
		p.NextCursor = encodeChatCursor(p.cursor)
	}
	slices.Reverse(p.Items)
	return p.conversationMessagePage
}

func (s *postgresStore) conversationMessagesPage(r *http.Request, p *principal, limit int) (conversationMessagePage, error) {
	var result conversationMessagePage
	id := r.PathValue("id")
	if !validChatID(id, "conv") {
		return result, conversationNotFound()
	}
	err := s.do(func(ctx context.Context, conn *pgx.Conn) error {
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		c := Conversation{ID: id, WorkspaceID: p.Workspace.ID, ProjectID: p.Project.ID}
		err = tx.QueryRow(ctx, `SELECT head_message_id,version FROM studio_conversations
 WHERE id=$1 AND workspace_id=$2 AND project_id=$3`, c.ID, c.WorkspaceID, c.ProjectID).Scan(&c.HeadMessageID, &c.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return conversationNotFound()
		}
		if err != nil {
			return err
		}
		page, err := newChatMessagePage(r, c)
		if err != nil {
			return err
		}
		ids, err := chatMessagePageIDsSQL(ctx, tx, c, page.cursor, limit)
		if err != nil {
			return err
		}
		for len(ids) > 0 && len(page.Items) < limit {
			node, err := chatPageMessageSQL(ctx, tx, c, ids[0])
			if err != nil {
				return err
			}
			fits, err := page.add(node)
			if err != nil {
				return err
			}
			if !fits {
				break
			}
			ids = ids[1:]
		}
		var next string
		if len(ids) != 0 {
			next = ids[0]
		}
		result = page.finish(next)
		return tx.Commit(ctx)
	})
	return result, err
}

// The v1 cursor is client-readable. Prove that its inclusive starting
// position belongs to this leaf before loading any user or assistant body.
func chatMessagePageIDsSQL(ctx context.Context, tx pgx.Tx, c Conversation, cursor conversationCursor, limit int) ([]string, error) {
	if cursor.Leaf == "" {
		if cursor.BeforeID != "" {
			return nil, invalidChatPageAncestor()
		}
		return nil, nil
	}
	start := cursor.BeforeID
	if start == "" {
		start = cursor.Leaf
	}
	var foundLeaf, foundStart bool
	var ids []string
	err := tx.QueryRow(ctx, `WITH RECURSIVE leaf AS (
 SELECT id FROM studio_chat_messages WHERE id=$1 AND conversation_id=$2 AND workspace_id=$3 AND project_id=$4
 ), boundary AS (
 SELECT id FROM leaf WHERE id=$5 OR studio_chat_is_ancestor(id,$5)
 ), page(id,parent_id,ordinal) AS (
 SELECT p.message_id,p.parent_id,1 FROM studio_chat_ancestry p WHERE p.message_id=$5 AND EXISTS(SELECT 1 FROM boundary)
 UNION ALL SELECT p.message_id,p.parent_id,a.ordinal+1 FROM page a JOIN studio_chat_ancestry p ON p.message_id=a.parent_id
 WHERE a.ordinal<$6
 ) SELECT EXISTS(SELECT 1 FROM leaf),EXISTS(SELECT 1 FROM boundary),ARRAY(SELECT id FROM page ORDER BY ordinal)`,
		cursor.Leaf, c.ID, c.WorkspaceID, c.ProjectID, start, limit+1).Scan(&foundLeaf, &foundStart, &ids)
	if err != nil {
		return nil, err
	}
	if !foundLeaf {
		return nil, conversationNotFound()
	}
	if !foundStart {
		return nil, invalidChatPageAncestor()
	}
	return ids, nil
}

// JSON escaping can make a page stop before its count limit. Fetch each body
// on demand, so only the first item that crosses the byte budget is probed.
func chatPageMessageSQL(ctx context.Context, tx pgx.Tx, c Conversation, id string) (MessageNode, error) {
	node := chatNode{MessageNode: MessageNode{ID: id, ConversationID: c.ID, Status: "COMPLETED"}}
	var document []byte
	err := tx.QueryRow(ctx, `SELECT m.parent_id,m.role,m.content,m.run_id,m.created_at,r.document
 FROM studio_chat_messages m LEFT JOIN studio_runs r
 ON r.id=m.run_id AND r.workspace_id=m.workspace_id AND r.project_id=m.project_id
 WHERE m.id=$1 AND m.conversation_id=$2 AND m.workspace_id=$3 AND m.project_id=$4`,
		id, c.ID, c.WorkspaceID, c.ProjectID).Scan(&node.ParentID, &node.Role, &node.Content, &node.RunID, &node.CreatedAt, &document)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessageNode{}, conversationNotFound()
	}
	if err != nil {
		return MessageNode{}, err
	}
	node.UpdatedAt = node.CreatedAt
	return hydrateChatNode(node, func(runID string) (Run, error) {
		var run Run
		if document == nil {
			return run, conversationNotFound()
		}
		if err := json.Unmarshal(document, &run); err != nil {
			return run, err
		}
		if run.ID != runID {
			return run, conversationNotFound()
		}
		return run, nil
	})
}
