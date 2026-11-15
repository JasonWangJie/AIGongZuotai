package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type libraryMutation struct {
	ExpectedRevision *uint64   `json:"expected_revision"`
	Name             *string   `json:"name,omitempty"`
	Tags             *[]string `json:"tags,omitempty"`
}

func validateLibraryMutation(current libraryRecord, input libraryMutation, action string) (libraryRecord, error) {
	if input.ExpectedRevision == nil || *input.ExpectedRevision < 1 || *input.ExpectedRevision > libraryRevisionLimit {
		return current, failure(422, "INVALID_ASSET_METADATA", "请提供有效的当前资产版本")
	}
	if *input.ExpectedRevision != current.Revision {
		return current, failure(409, "ASSET_REVISION_CONFLICT", "资产已被其他操作更新，请先读取最新版本")
	}
	if current.Revision >= libraryRevisionLimit {
		return current, failure(429, "ASSET_REVISION_LIMIT", "资产元数据历史已达到上限")
	}
	next := current
	next.LibraryEntry = copyLibraryEntry(current.LibraryEntry)
	switch action {
	case "update":
		if current.State == "trashed" {
			return current, failure(409, "ASSET_STATE_CONFLICT", "请先恢复资产，再编辑名称和标签")
		}
		if input.Name == nil || input.Tags == nil {
			return current, failure(422, "INVALID_ASSET_METADATA", "名称与标签必须同时提供")
		}
		name := strings.TrimSpace(*input.Name)
		if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 120 || hasControl(name) || len(*input.Tags) > 10 {
			return current, failure(422, "INVALID_ASSET_METADATA", "名称须为1至120字符，标签最多10个")
		}
		tags := []string{}
		seen := map[string]bool{}
		for _, value := range *input.Tags {
			tag := strings.ToLower(strings.TrimSpace(value))
			if utf8.RuneCountInString(tag) < 1 || utf8.RuneCountInString(tag) > 32 || hasControl(tag) {
				return current, failure(422, "INVALID_ASSET_METADATA", "每个标签须为1至32字符且不含控制字符")
			}
			if !seen[tag] {
				tags = append(tags, tag)
				seen[tag] = true
			}
		}
		next.Name, next.Tags = name, tags
	case "trash", "restore":
		if input.Name != nil || input.Tags != nil {
			return current, failure(422, "INVALID_ASSET_METADATA", "回收站操作只接受当前版本")
		}
		if action == "trash" {
			if current.Revision >= libraryRevisionLimit-1 {
				return current, failure(429, "ASSET_REVISION_LIMIT", "历史空间不足，已为恢复操作保留一个版本")
			}
			if current.State != "active" {
				return current, failure(409, "ASSET_STATE_CONFLICT", "资产已经在回收站")
			}
			next.State = "trashed"
		} else {
			if current.State != "trashed" {
				return current, failure(409, "ASSET_STATE_CONFLICT", "资产不在回收站")
			}
			next.State = "active"
		}
	default:
		return current, failure(422, "INVALID_ASSET_METADATA", "不支持此资产操作")
	}
	next.Revision++
	next.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	if next.State == "trashed" {
		if next.DeletedAt == nil {
			v := next.UpdatedAt
			next.DeletedAt = &v
		}
	} else {
		next.DeletedAt = nil
	}
	return next, nil
}
func libraryActionAudit(entry libraryRecord, old libraryRecord, action, actor string) AuditEvent {
	actions := map[string]string{"update": "asset.updated", "trash": "asset.trashed", "restore": "asset.restored"}
	return newAuditEvent(entry.WorkspaceID, entry.ProjectID, actor, actions[action], "asset", entry.ID, AuditDetails{FromStatus: old.State, ToStatus: entry.State, Revision: entry.Revision, Kind: entry.Kind})
}
func mutateLibrarySQL(ctx context.Context, tx pgx.Tx, workspaceID, projectID, id, key, action, actor string, input libraryMutation, hash [32]byte) (LibraryEntry, bool, error) {
	if !libraryIDPattern.MatchString(id) {
		return LibraryEntry{}, false, failure(404, "ASSET_NOT_FOUND", "资产不存在或不属于当前项目")
	}
	// Also serializes the workspace-wide idempotency key across different assets.
	var locked string
	if err := tx.QueryRow(ctx, "SELECT id FROM studio_workspaces WHERE id=$1 FOR UPDATE", workspaceID).Scan(&locked); err != nil {
		return LibraryEntry{}, false, err
	}
	var oldHash, response []byte
	err := tx.QueryRow(ctx, "SELECT payload_hash,response FROM studio_library_idempotency WHERE workspace_id=$1 AND idempotency_key=$2", workspaceID, key).Scan(&oldHash, &response)
	if err == nil {
		var h [32]byte
		copy(h[:], oldHash)
		if h != hash {
			return LibraryEntry{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已用于其他资产操作")
		}
		var entry LibraryEntry
		err = json.Unmarshal(response, &entry)
		return entry, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return LibraryEntry{}, false, err
	}
	current, err := scanLibrary(tx.QueryRow(ctx, "SELECT "+libraryColumns+" FROM studio_library_entries WHERE id=$1 AND workspace_id=$2 AND project_id=$3 FOR UPDATE", id, workspaceID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "ASSET_NOT_FOUND", "资产不存在或不属于当前项目")
	}
	if err != nil {
		return LibraryEntry{}, false, err
	}
	next, err := validateLibraryMutation(current, input, action)
	if err != nil {
		return LibraryEntry{}, false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE studio_library_entries SET name=$2,tags=$3,state=$4,revision=$5,updated_at=$6,deleted_at=$7 WHERE id=$1", id, next.Name, next.Tags, next.State, next.Revision, next.UpdatedAt, next.DeletedAt); err != nil {
		return LibraryEntry{}, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO studio_library_revisions(asset_id,project_id,workspace_id,revision,name,tags,state,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", id, projectID, workspaceID, next.Revision, next.Name, next.Tags, next.State, next.UpdatedAt); err != nil {
		return LibraryEntry{}, false, err
	}
	entry := next.public()
	response, err = json.Marshal(entry)
	if err != nil {
		return LibraryEntry{}, false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO studio_library_idempotency(workspace_id,idempotency_key,payload_hash,asset_id,revision,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)", workspaceID, key, hash[:], id, next.Revision, response, next.UpdatedAt); err != nil {
		return LibraryEntry{}, false, err
	}
	if err = insertAuditSQL(ctx, tx, libraryActionAudit(next, current, action, actor)); err != nil {
		return LibraryEntry{}, false, err
	}
	return entry, false, nil
}
func (a *App) mutateLibrary(workspaceID, projectID, id, key, action, actor string, input libraryMutation) (LibraryEntry, bool, error) {
	hash := payloadHash(struct {
		ProjectID, ID, Action string
		Input                 libraryMutation
	}{projectID, id, action, input})
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return LibraryEntry{}, false, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	if a.store != nil {
		var entry LibraryEntry
		var replay bool
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			entry, replay, err = mutateLibrarySQL(ctx, tx, workspaceID, projectID, id, key, action, actor, input, hash)
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		})
		return entry, replay, err
	}
	index := workspaceID + ":" + key
	if old, found := a.libraryIdem[index]; found {
		if old.Hash != hash {
			return LibraryEntry{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "相同幂等键已用于其他资产操作")
		}
		return copyLibraryEntry(old.Entry), true, nil
	}
	current, err := a.loadLibraryLocked(id, workspaceID, projectID)
	if err != nil {
		return LibraryEntry{}, false, err
	}
	next, err := validateLibraryMutation(current, input, action)
	if err != nil {
		return LibraryEntry{}, false, err
	}
	reserved := a.memoryLibraryReservedRestoresLocked()
	if action == "trash" {
		reserved++
	} else if action == "restore" {
		reserved--
	}
	if a.libraryRevisionCount+1+reserved > memoryLibraryRevisionLimit || len(a.libraryIdem) >= memoryLibraryRevisionLimit {
		return LibraryEntry{}, false, failure(503, "LIBRARY_MEMORY_FULL", "本地资产元数据历史已满，请使用持久数据库")
	}
	a.library[id] = next
	a.libraryRevisions[id] = append(a.libraryRevisions[id], libraryRevision(next.LibraryEntry))
	a.libraryRevisionCount++
	entry := next.public()
	a.libraryIdem[index] = libraryReplay{Hash: hash, Entry: copyLibraryEntry(entry)}
	a.appendMemoryAuditLocked(libraryActionAudit(next, current, action, actor))
	return entry, false, nil
}
func (a *App) mutateLibraryHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input libraryMutation
	if err = decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	action := "update"
	if strings.HasSuffix(r.URL.Path, "/trash") {
		action = "trash"
	} else if strings.HasSuffix(r.URL.Path, "/restore") {
		action = "restore"
	}
	p := principalFrom(r)
	entry, replayed, err := a.mutateLibrary(p.Workspace.ID, p.Project.ID, r.PathValue("id"), key, action, auditActorID(r), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, entry)
}
func (a *App) listLibraryRevisionsHTTP(w http.ResponseWriter, r *http.Request) {
	limit, before := 20, uint64(libraryRevisionLimit+1)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 50 {
			writeError(w, invalidLibraryQuery())
			return
		}
		limit = v
	}
	if raw := r.URL.Query().Get("before_revision"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || v == 0 || v > libraryRevisionLimit+1 {
			writeError(w, invalidLibraryQuery())
			return
		}
		before = v
	}
	p := principalFrom(r)
	items := []LibraryRevision{}
	a.mu.Lock()
	entry, err := a.loadLibraryLocked(r.PathValue("id"), p.Workspace.ID, p.Project.ID)
	if err == nil && a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			rows, e := conn.Query(ctx, "SELECT revision,name,tags,state,updated_at FROM studio_library_revisions WHERE asset_id=$1 AND workspace_id=$2 AND project_id=$3 AND revision<$4 ORDER BY revision DESC LIMIT $5", entry.ID, p.Workspace.ID, p.Project.ID, before, limit+1)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var v LibraryRevision
				if e = rows.Scan(&v.Revision, &v.Name, &v.Tags, &v.State, &v.UpdatedAt); e != nil {
					return e
				}
				items = append(items, v)
			}
			return rows.Err()
		})
	} else if err == nil {
		history := a.libraryRevisions[entry.ID]
		for i := len(history) - 1; i >= 0 && len(items) < limit+1; i-- {
			if history[i].Revision < before {
				v := history[i]
				v.Tags = append([]string{}, v.Tags...)
				items = append(items, v)
			}
		}
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next *uint64
	if more {
		v := items[len(items)-1].Revision
		next = &v
	}
	writeJSON(w, 200, map[string]any{"items": items, "has_more": more, "next_before_revision": next})
}

func (a *App) serveLibraryHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	a.mu.Lock()
	entry, err := a.loadLibraryLocked(r.PathValue("id"), p.Workspace.ID, p.Project.ID)
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	thumbnail := strings.HasSuffix(r.URL.Path, "/thumbnail")
	if entry.State != "active" || !libraryContentReleased(entry) || thumbnail && !entry.HasThumbnail {
		writeError(w, failure(404, "ASSET_NOT_FOUND", "资产内容不存在或已在回收站"))
		return
	}
	if entry.Source == "reference" {
		r.SetPathValue("id", entry.ReferenceID)
		a.serveReference(w, r)
		return
	}
	if entry.ProviderMode == "demo" && strings.HasPrefix(entry.OriginalURL, "/demo-assets/") {
		r.SetPathValue("file", strings.TrimPrefix(entry.OriginalURL, "/demo-assets/"))
		serveDemoAsset(w, r)
		return
	}
	if thumbnail {
		a.assets.ServeThumbnail(w, r, entry.ArtifactID)
	} else {
		a.assets.ServeAsset(w, r, entry.ArtifactID)
	}
}
func (a *App) legacyAssetAllowedLocked(workspaceID, projectID, id string) (bool, error) {
	if !localAssetID.MatchString(id) {
		return false, nil
	}
	if a.store == nil {
		for _, entry := range a.library {
			if entry.WorkspaceID == workspaceID && (projectID == "" || entry.ProjectID == projectID) && entry.Source == "generated" && entry.ArtifactID == id && entry.State == "active" && libraryContentReleased(entry) {
				return true, nil
			}
		}
		return false, nil
	}
	return a.store.assetOwned(workspaceID, projectID, id)
}
