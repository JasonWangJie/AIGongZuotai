package studio

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type LibraryEntry struct {
	Moderation   *ModerationSummary `json:"moderation,omitempty"`
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	Source       string             `json:"source"`
	ReferenceID  string             `json:"reference_id,omitempty"`
	TaskID       string             `json:"task_id,omitempty"`
	ArtifactID   string             `json:"artifact_id,omitempty"`
	ProviderMode string             `json:"provider_mode,omitempty"`
	Kind         string             `json:"kind"`
	Name         string             `json:"name"`
	Tags         []string           `json:"tags"`
	State        string             `json:"state"`
	Revision     uint64             `json:"revision"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	DeletedAt    *time.Time         `json:"deleted_at"`
	ContentURL   *string            `json:"content_url"`
	ThumbnailURL *string            `json:"thumbnail_url"`
	ContentType  string             `json:"content_type,omitempty"`
	ByteSize     int64              `json:"byte_size,omitempty"`
}
type LibraryRevision struct {
	Revision  uint64    `json:"revision"`
	Name      string    `json:"name"`
	Tags      []string  `json:"tags"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}
type libraryRecord struct {
	LibraryEntry
	ModerationTaskStatus string
	WorkspaceID          string
	HasThumbnail         bool
	OriginalURL          string
	StorageInfo          *AssetInfo
}
type libraryReplay struct {
	Hash  [32]byte
	Entry LibraryEntry
}

const memoryLibraryLimit = 4096
const memoryLibraryRevisionLimit = 16384
const libraryRevisionLimit = 1000

var libraryIDPattern = regexp.MustCompile(`^lib_[a-f0-9]{64}$`)

func libraryID(source, sourceID, artifactID string) string {
	identity := source + ":" + sourceID
	if source == "generated" {
		identity += ":" + artifactID
	}
	hash := sha256.Sum256([]byte(identity))
	return "lib_" + hex.EncodeToString(hash[:])
}
func copyLibraryEntry(entry LibraryEntry) LibraryEntry {
	entry.Moderation = copyRunModeration(entry.Moderation)
	entry.Tags = append([]string{}, entry.Tags...)
	if entry.DeletedAt != nil {
		v := *entry.DeletedAt
		entry.DeletedAt = &v
	}
	if entry.ContentURL != nil {
		v := *entry.ContentURL
		entry.ContentURL = &v
	}
	if entry.ThumbnailURL != nil {
		v := *entry.ThumbnailURL
		entry.ThumbnailURL = &v
	}
	return entry
}
func (entry libraryRecord) public() LibraryEntry {
	v := copyLibraryEntry(entry.LibraryEntry)
	v.ContentURL, v.ThumbnailURL = nil, nil
	if v.State == "active" && libraryContentReleased(entry) {
		base := "/api/v1/library/" + url.PathEscape(v.ID)
		query := "?project_id=" + url.QueryEscape(v.ProjectID)
		content := base + "/content" + query
		v.ContentURL = &content
		if entry.HasThumbnail {
			thumb := base + "/thumbnail" + query
			v.ThumbnailURL = &thumb
		}
	}
	return v
}
func libraryRevision(entry LibraryEntry) LibraryRevision {
	return LibraryRevision{Revision: entry.Revision, Name: entry.Name, Tags: append([]string{}, entry.Tags...), State: entry.State, UpdatedAt: entry.UpdatedAt}
}
func libraryFromReference(ref referenceRecord) libraryRecord {
	return libraryRecord{LibraryEntry: LibraryEntry{Moderation: copyRunModeration(ref.Moderation), ID: libraryID("reference", ref.ID, ""), ProjectID: ref.ProjectID, Source: "reference", ReferenceID: ref.ID, Kind: "image", Name: ref.Name, Tags: []string{}, State: "active", Revision: 1, CreatedAt: ref.CreatedAt, UpdatedAt: ref.CreatedAt, ContentType: ref.Blob.ContentType, ByteSize: ref.Blob.ByteSize}, WorkspaceID: ref.WorkspaceID, HasThumbnail: ref.Blob.Thumbnail != nil}
}
func libraryFromArtifact(task Task, artifact Artifact) libraryRecord {
	kind := artifact.Kind
	if kind != "image" && kind != "video" && kind != "poster" {
		kind = "image"
	}
	name := referenceName(artifact.Title)
	if strings.TrimSpace(artifact.Title) == "" {
		name = "生成作品"
	}
	mode := task.ProviderMode
	if mode == "" {
		mode = "demo"
	}
	entry := libraryRecord{LibraryEntry: LibraryEntry{ID: libraryID("generated", task.ID, artifact.ID), ProjectID: task.ProjectID, Source: "generated", TaskID: task.ID, ArtifactID: artifact.ID, ProviderMode: mode, Kind: kind, Name: name, Tags: []string{}, State: "active", Revision: 1, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}, WorkspaceID: task.WorkspaceID, OriginalURL: artifact.URL, StorageInfo: artifact.StorageInfo, HasThumbnail: mode == "demo" && strings.HasPrefix(artifact.URL, "/demo-assets/")}
	entry.Moderation, entry.ModerationTaskStatus = copyRunModeration(task.Moderation), task.Status
	if info := artifact.StorageInfo; info != nil {
		entry.ContentType, entry.ByteSize = info.ContentType, info.ByteSize
		entry.HasThumbnail = info.Thumbnail != nil
	}
	return entry
}
func (a *App) registerMemoryLibraryLocked(entry libraryRecord) {
	if a.store != nil {
		return
	}
	if _, exists := a.library[entry.ID]; exists {
		return
	}
	a.library[entry.ID] = entry
	a.libraryRevisions[entry.ID] = []LibraryRevision{libraryRevision(entry.LibraryEntry)}
	a.libraryRevisionCount++
}
func (a *App) registerMemoryTaskLibraryLocked(task Task) {
	for _, artifact := range task.Artifacts {
		if artifact.ID != "" {
			a.registerMemoryLibraryLocked(libraryFromArtifact(task, artifact))
		}
	}
}
func (a *App) checkMemoryLibraryCapacityLocked(extra int) error {
	if a.store != nil {
		return nil
	}
	pending := 0
	for _, record := range a.tasks {
		if terminal(record.Task.Status) {
			continue
		}
		count := record.Input.Parameters.Count
		if count < 1 {
			count = 1
		}
		if count > 4 {
			count = 4
		}
		pending += max(0, count-len(record.Task.Artifacts))
	}
	if len(a.library)+pending+extra > memoryLibraryLimit || a.libraryRevisionCount+pending+extra+a.memoryLibraryReservedRestoresLocked() > memoryLibraryRevisionLimit {
		return failure(503, "LIBRARY_MEMORY_FULL", "本地内存资产库容量已满；持久使用请配置PostgreSQL")
	}
	return nil
}

func (a *App) memoryLibraryReservedRestoresLocked() int {
	reserved := 0
	for _, entry := range a.library {
		if entry.State == "trashed" {
			reserved++
		}
	}
	return reserved
}

const libraryColumns = `id,workspace_id,project_id,source,coalesce(reference_id,''),coalesce(task_id,''),coalesce(artifact_id,''),coalesce(provider_mode,''),kind,name,tags,state,revision,created_at,updated_at,deleted_at,coalesce(content_type,''),coalesce(byte_size,0),has_thumbnail,original_url,storage_info,` + libraryModerationProjection + `,coalesce((SELECT document->>'status' FROM studio_tasks t WHERE t.id=studio_library_entries.task_id),'')`

const libraryModerationProjection = `coalesce(
 (SELECT jsonb_build_object('policy_id',m.policy_id,'policy_version',m.policy_version,'phase','output','state',m.state,'version',m.version,'reason_code',m.reason_code) FROM studio_task_media_moderation m WHERE m.task_id=studio_library_entries.task_id),
 (SELECT document->'moderation' FROM studio_tasks t WHERE t.id=studio_library_entries.task_id),
 (SELECT jsonb_build_object('policy_id',m.policy_id,'policy_version',m.policy_version,'phase','input','state',m.state,'version',m.version,'reason_code',m.reason_code) FROM studio_reference_media_moderation m WHERE m.reference_id=studio_library_entries.reference_id))`

func libraryContentReleased(entry libraryRecord) bool {
	return entry.Moderation == nil || entry.Moderation.State == "ALLOWED" && (entry.Source != "generated" || entry.ModerationTaskStatus == "SUCCEEDED" || entry.ModerationTaskStatus == "PARTIAL")
}

type libraryScanner interface{ Scan(...any) error }

func scanLibrary(row libraryScanner) (libraryRecord, error) {
	var e libraryRecord
	var storage, moderation []byte
	err := row.Scan(&e.ID, &e.WorkspaceID, &e.ProjectID, &e.Source, &e.ReferenceID, &e.TaskID, &e.ArtifactID, &e.ProviderMode, &e.Kind, &e.Name, &e.Tags, &e.State, &e.Revision, &e.CreatedAt, &e.UpdatedAt, &e.DeletedAt, &e.ContentType, &e.ByteSize, &e.HasThumbnail, &e.OriginalURL, &storage, &moderation, &e.ModerationTaskStatus)
	if err == nil && len(storage) > 0 {
		err = json.Unmarshal(storage, &e.StorageInfo)
	}
	if err == nil && len(moderation) > 0 {
		err = json.Unmarshal(moderation, &e.Moderation)
	}
	return e, err
}
func insertLibrarySQL(ctx context.Context, tx pgx.Tx, e libraryRecord) error {
	storage, err := json.Marshal(e.StorageInfo)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_library_entries(id,workspace_id,project_id,source,reference_id,task_id,artifact_id,provider_mode,kind,name,tags,state,revision,created_at,updated_at,content_type,byte_size,has_thumbnail,original_url,storage_info)
 VALUES($1,$2,$3,$4,nullif($5,''),nullif($6,''),nullif($7,''),nullif($8,''),$9,$10,$11,'active',1,$12,$13,nullif($14,''),nullif($15,0),$16,$17,nullif($18::jsonb,'null'::jsonb))
 ON CONFLICT(id) DO UPDATE SET storage_info=coalesce(studio_library_entries.storage_info,excluded.storage_info),content_type=coalesce(studio_library_entries.content_type,excluded.content_type),byte_size=coalesce(studio_library_entries.byte_size,excluded.byte_size),has_thumbnail=studio_library_entries.has_thumbnail OR excluded.has_thumbnail`, e.ID, e.WorkspaceID, e.ProjectID, e.Source, e.ReferenceID, e.TaskID, e.ArtifactID, e.ProviderMode, e.Kind, e.Name, e.Tags, e.CreatedAt, e.UpdatedAt, e.ContentType, e.ByteSize, e.HasThumbnail, e.OriginalURL, storage)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO studio_library_revisions(asset_id,workspace_id,project_id,revision,name,tags,state,updated_at) SELECT id,workspace_id,project_id,revision,name,tags,state,updated_at FROM studio_library_entries WHERE id=$1 AND revision=1 ON CONFLICT DO NOTHING`, e.ID)
	return err
}
func registerTaskLibrarySQL(ctx context.Context, tx pgx.Tx, task Task) error {
	for _, artifact := range task.Artifacts {
		if artifact.ID != "" {
			if err := insertLibrarySQL(ctx, tx, libraryFromArtifact(task, artifact)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *App) loadLibraryLocked(id, workspaceID, projectID string) (libraryRecord, error) {
	var entry libraryRecord
	if !libraryIDPattern.MatchString(id) {
		return entry, failure(404, "ASSET_NOT_FOUND", "资产不存在或不属于当前项目")
	}
	if a.store == nil {
		var found bool
		entry, found = a.library[id]
		if !found || entry.WorkspaceID != workspaceID || entry.ProjectID != projectID {
			return entry, failure(404, "ASSET_NOT_FOUND", "资产不存在或不属于当前项目")
		}
		return entry, nil
	}
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var err error
		entry, err = scanLibrary(conn.QueryRow(ctx, "SELECT "+libraryColumns+" FROM studio_library_entries WHERE id=$1 AND workspace_id=$2 AND project_id=$3", id, workspaceID, projectID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "ASSET_NOT_FOUND", "资产不存在或不属于当前项目")
	}
	return entry, err
}
func (a *App) referenceActiveLocked(id, workspaceID, projectID string) error {
	entry, err := a.loadLibraryLocked(libraryID("reference", id, ""), workspaceID, projectID)
	if err != nil {
		return err
	}
	if entry.State != "active" {
		return failure(404, "ASSET_NOT_FOUND", "参考素材已在回收站，请先恢复")
	}
	return nil
}

type libraryFilter struct {
	WorkspaceID string `json:"w"`
	ProjectID   string `json:"p"`
	State       string `json:"s"`
	Source      string `json:"o"`
	Kind        string `json:"k"`
	Query       string `json:"q"`
	Tag         string `json:"g"`
}
type libraryCursor struct {
	Filter    libraryFilter `json:"f"`
	CreatedAt time.Time     `json:"t"`
	ID        string        `json:"i"`
}

func invalidLibraryQuery() error {
	return failure(400, "INVALID_LIBRARY_QUERY", "资产筛选或分页参数无效")
}
func libraryQuery(r *http.Request) (libraryFilter, int, *libraryCursor, error) {
	p := principalFrom(r)
	q := r.URL.Query()
	if !utf8.ValidString(q.Get("q")) || !utf8.ValidString(q.Get("tag")) {
		return libraryFilter{}, 0, nil, invalidLibraryQuery()
	}
	f := libraryFilter{WorkspaceID: p.Workspace.ID, ProjectID: p.Project.ID, State: q.Get("state"), Source: q.Get("source"), Kind: q.Get("kind"), Query: strings.TrimSpace(q.Get("q")), Tag: strings.ToLower(strings.TrimSpace(q.Get("tag")))}
	if f.State == "" {
		f.State = "active"
	}
	if (f.State != "active" && f.State != "trashed") || (f.Source != "" && f.Source != "reference" && f.Source != "generated") || (f.Kind != "" && f.Kind != "image" && f.Kind != "video" && f.Kind != "poster") || utf8.RuneCountInString(f.Query) > 128 || utf8.RuneCountInString(f.Tag) > 32 || hasControl(f.Query) || hasControl(f.Tag) {
		return f, 0, nil, invalidLibraryQuery()
	}
	limit := 30
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 100 {
			return f, 0, nil, invalidLibraryQuery()
		}
		limit = v
	}
	var cursor *libraryCursor
	if raw := q.Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			return f, 0, nil, invalidLibraryQuery()
		}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return f, 0, nil, invalidLibraryQuery()
		}
		var c libraryCursor
		if err = json.Unmarshal(data, &c); err != nil || c.Filter != f || c.CreatedAt.IsZero() || !libraryIDPattern.MatchString(c.ID) {
			return f, 0, nil, invalidLibraryQuery()
		}
		cursor = &c
	}
	return f, limit, cursor, nil
}
func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
func (a *App) listLibraryHTTP(w http.ResponseWriter, r *http.Request) {
	filter, limit, cursor, err := libraryQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	entries := []libraryRecord{}
	a.mu.Lock()
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			args := []any{filter.WorkspaceID, filter.ProjectID, filter.State}
			where := "workspace_id=$1 AND project_id=$2 AND state=$3"
			add := func(clause string, value any) { args = append(args, value); where += fmt.Sprintf(clause, len(args)) }
			if filter.Source != "" {
				add(" AND source=$%d", filter.Source)
			}
			if filter.Kind != "" {
				add(" AND kind=$%d", filter.Kind)
			}
			if filter.Tag != "" {
				add(" AND $%d=ANY(tags)", filter.Tag)
			}
			if filter.Query != "" {
				args = append(args, strings.ToLower(filter.Query))
				n := len(args)
				where += fmt.Sprintf(" AND (strpos(lower(name),$%d)>0 OR EXISTS(SELECT 1 FROM unnest(tags) tag WHERE strpos(lower(tag),$%d)>0))", n, n)
			}
			if cursor != nil {
				args = append(args, cursor.CreatedAt, cursor.ID)
				where += fmt.Sprintf(" AND (created_at,id)<($%d,$%d)", len(args)-1, len(args))
			}
			args = append(args, limit+1)
			rows, e := conn.Query(ctx, "SELECT "+libraryColumns+" FROM studio_library_entries WHERE "+where+fmt.Sprintf(" ORDER BY created_at DESC,id DESC LIMIT $%d", len(args)), args...)
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				v, e := scanLibrary(rows)
				if e != nil {
					return e
				}
				entries = append(entries, v)
			}
			return rows.Err()
		})
	} else {
		for _, e := range a.library {
			if e.WorkspaceID != filter.WorkspaceID || e.ProjectID != filter.ProjectID || e.State != filter.State || filter.Source != "" && e.Source != filter.Source || filter.Kind != "" && e.Kind != filter.Kind {
				continue
			}
			tagMatch := filter.Tag == ""
			queryMatch := filter.Query == "" || strings.Contains(strings.ToLower(e.Name), strings.ToLower(filter.Query))
			for _, tag := range e.Tags {
				tagMatch = tagMatch || tag == filter.Tag
				queryMatch = queryMatch || strings.Contains(tag, strings.ToLower(filter.Query))
			}
			if !tagMatch || !queryMatch {
				continue
			}
			if cursor != nil && (e.CreatedAt.After(cursor.CreatedAt) || e.CreatedAt.Equal(cursor.CreatedAt) && e.ID >= cursor.ID) {
				continue
			}
			entries = append(entries, e)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
				return entries[i].ID > entries[j].ID
			}
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		})
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	items := make([]LibraryEntry, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entry.public())
	}
	var next *string
	if hasMore {
		last := entries[len(entries)-1]
		data, _ := json.Marshal(libraryCursor{Filter: filter, CreatedAt: last.CreatedAt, ID: last.ID})
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "has_more": hasMore, "limit": limit})
}
func (a *App) getLibraryHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	a.mu.Lock()
	entry, err := a.loadLibraryLocked(r.PathValue("id"), p.Workspace.ID, p.Project.ID)
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, entry.public())
}
