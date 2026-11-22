package studio

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const (
	longConversationNodeLimit = 32768
	longConversationPathLimit = 16384
)

type contextAncestor struct {
	id    string
	bytes int
}

type persistentChatHistory struct {
	depth int
	nodes []chatNode
	runs  map[string]Run
}

func preparePersistentConversationTurn(ctx context.Context, tx pgx.Tx, c Conversation, input ConversationRunInput, prompt string, model catalogEntry, long bool) (Run, chatNode, chatNode, *chatExecution, error) {
	history, err := persistentChatPathSQL(ctx, tx, c, stringID(input.ParentMessageID), prompt, model)
	if err != nil {
		return Run{}, chatNode{}, chatNode{}, nil, err
	}
	load := func(id string) (Run, error) {
		r, ok := history.runs[id]
		if !ok {
			return Run{}, conversationNotFound()
		}
		return r, nil
	}
	edit := func(id string) (chatNode, error) {
		return readChatNodeSQL(ctx, tx, c.WorkspaceID, c.ProjectID, c.ID, id)
	}
	nodeLimit, pathLimit := conversationNodeLimit, conversationPathLimit
	if long {
		nodeLimit, pathLimit = longConversationNodeLimit, longConversationPathLimit
	}
	return prepareBoundedConversationTurn(c, input, prompt, model, history.nodes, load, edit, history.depth, nodeLimit, pathLimit)
}

// Old pairs add complete user/assistant messages. Their exact UTF-8 sizes
// allow us to stop at the same first over-budget pair as ValidateChatRequest,
// before reading those bodies. The ordinary validator runs again on the real
// texts and builds the immutable execution request used for moderation.
func chatPairFits(bodyBytes, messageCount int, model catalogEntry) bool {
	return messageCount <= MaxChatMessages && bodyBytes <= MaxChatContentBytes &&
		bodyBytes+messageCount*ChatMessageOverhead+ChatSafetyReserve+model.Model.Capabilities.MaxOutputTokens <= model.Model.Capabilities.ContextWindowTokens
}

func persistentChatPathSQL(ctx context.Context, tx pgx.Tx, c Conversation, leaf, prompt string, model catalogEntry) (persistentChatHistory, error) {
	h := persistentChatHistory{runs: make(map[string]Run)}
	if leaf == "" {
		return h, nil
	}
	var head chatNode
	head.ID = leaf
	err := tx.QueryRow(ctx, `SELECT m.role,m.run_id,m.parent_id,p.depth FROM studio_chat_messages m
		JOIN studio_chat_ancestry p ON p.message_id=m.id WHERE m.id=$1 AND m.conversation_id=$2 AND m.workspace_id=$3 AND m.project_id=$4`,
		leaf, c.ID, c.WorkspaceID, c.ProjectID).Scan(&head.Role, &head.RunID, &head.ParentID, &h.depth)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, conversationNotFound()
	}
	if err != nil {
		return h, err
	}
	h.nodes = append(h.nodes, head)
	if head.Role != "assistant" {
		return h, nil
	} // the caller reports INVALID_CHAT_PARENT
	// The immediate parent is also the explicit "continue" target. Read
	// its real status and decision even when it must be skipped, while
	// keeping both bodies of a quarantined or empty pair out of this window.
	var user chatNode
	var direct Run
	err = tx.QueryRow(ctx, `SELECT u.id,u.role,
		CASE WHEN studio_chat_released_output(r.document)<>'' THEN u.content ELSE '' END,
		r.id,r.document->>'status',r.document->'moderation',studio_chat_released_output(r.document)
		FROM studio_chat_messages u JOIN studio_runs r ON r.id=$2 AND r.conversation_id=u.conversation_id
		AND r.workspace_id=u.workspace_id AND r.project_id=u.project_id
		WHERE u.id=$1 AND u.role='user' AND u.conversation_id=$3 AND u.workspace_id=$4 AND u.project_id=$5`,
		stringID(head.ParentID), stringID(head.RunID), c.ID, c.WorkspaceID, c.ProjectID).Scan(&user.ID, &user.Role, &user.Content, &direct.ID, &direct.Status, &direct.Moderation, &direct.Output)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, conversationNotFound()
	}
	if err != nil {
		return h, err
	}
	h.nodes = append(h.nodes, user)
	h.runs[direct.ID] = direct
	messages, bytes := 1, len(prompt)
	if !terminal(direct.Status) {
		return h, nil
	}
	if runOutputReleased(direct) && direct.Output != "" {
		messages += 2
		bytes += len(user.Content) + len(direct.Output)
		if !chatPairFits(bytes, messages, model) {
			return h, nil
		}
	}
	remaining := (MaxChatMessages - messages) / 2
	rows, err := tx.Query(ctx, `WITH RECURSIVE candidates(id,byte_count,ordinal) AS (
	 SELECT eligible_id,eligible_bytes,1 FROM studio_chat_ancestry WHERE message_id=$1 AND eligible_id IS NOT NULL
	 UNION ALL SELECT p.eligible_id,p.eligible_bytes,c.ordinal+1 FROM candidates c
	 JOIN studio_chat_ancestry p ON p.message_id=c.id WHERE p.eligible_id IS NOT NULL AND c.ordinal<$2
	) SELECT id,byte_count FROM candidates ORDER BY ordinal`, head.ID, remaining)
	if err != nil {
		return h, err
	}
	var wanted []contextAncestor
	var ids []string
	for rows.Next() {
		var pair contextAncestor
		if err = rows.Scan(&pair.id, &pair.bytes); err != nil {
			rows.Close()
			return h, err
		}
		if !chatPairFits(bytes+pair.bytes, messages+2, model) {
			break
		}
		messages += 2
		bytes += pair.bytes
		wanted = append(wanted, pair)
		ids = append(ids, pair.id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return h, err
	}
	if len(wanted) == 0 {
		return h, nil
	}
	rows, err = tx.Query(ctx, `SELECT a.id,u.id,CASE WHEN studio_chat_released_output(r.document)<>'' THEN u.content ELSE '' END,
		r.id,r.document->>'status',r.document->'moderation',studio_chat_released_output(r.document)
	 FROM studio_chat_messages a JOIN studio_chat_messages u ON u.id=a.parent_id AND u.role='user'
	 JOIN studio_runs r ON r.id=a.run_id AND r.workspace_id=a.workspace_id AND r.project_id=a.project_id AND r.conversation_id=a.conversation_id
	 WHERE a.id=ANY($1::text[]) AND a.role='assistant' AND a.workspace_id=$2 AND a.project_id=$3 AND a.conversation_id=$4`, ids, c.WorkspaceID, c.ProjectID, c.ID)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	type hydratedPair struct{ user, assistant chatNode }
	pairs := make(map[string]hydratedPair, len(wanted))
	for rows.Next() {
		var a, u chatNode
		var r Run
		if err = rows.Scan(&a.ID, &u.ID, &u.Content, &r.ID, &r.Status, &r.Moderation, &r.Output); err != nil {
			return h, err
		}
		a.Role, u.Role, a.RunID = "assistant", "user", &r.ID
		if !terminal(r.Status) || !runOutputReleased(r) || r.Output == "" {
			return h, errors.New("conversation history eligibility changed")
		}
		h.runs[r.ID] = r
		pairs[a.ID] = hydratedPair{u, a}
	}
	if err = rows.Err(); err != nil {
		return h, err
	}
	for _, expected := range wanted {
		value, ok := pairs[expected.id]
		if !ok {
			return h, conversationNotFound()
		}
		if len(value.user.Content)+len(h.runs[*value.assistant.RunID].Output) != expected.bytes {
			return h, errors.New("conversation history size changed")
		}
		h.nodes = append(h.nodes, value.assistant, value.user)
	}
	return h, nil
}
