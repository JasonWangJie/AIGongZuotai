package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const referenceModerationProjection = `(SELECT jsonb_build_object('policy_id',m.policy_id,'policy_version',m.policy_version,'phase','input','state',m.state,'version',m.version,'reason_code',m.reason_code) FROM studio_reference_media_moderation m WHERE m.reference_id=studio_references.id)`

const maxWorkspaceReferenceBytes = int64(128 << 20)
const maxWorkspaceReferenceFiles = 100

var referenceAssetID = regexp.MustCompile(`^ref_[a-f0-9]{32}$`)

type ReferenceAsset struct {
	Moderation   *ModerationSummary `json:"moderation,omitempty"`
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	Name         string             `json:"name"`
	ContentType  string             `json:"content_type"`
	ByteSize     int64              `json:"byte_size"`
	Width        int                `json:"width"`
	Height       int                `json:"height"`
	CreatedAt    time.Time          `json:"created_at"`
	URL          string             `json:"url"`
	ThumbnailURL string             `json:"thumbnail_url"`
}
type referenceRecord struct {
	ModerationPolicy                 *MediaModerationPolicy
	Moderation                       *ModerationSummary
	ID, WorkspaceID, ProjectID, Name string
	Blob                             ReferenceBlob
	StoredBytes                      int64
	CreatedAt                        time.Time
}

func (r referenceRecord) public() ReferenceAsset {
	base := "/api/v1/references/" + url.PathEscape(r.ID)
	query := "?project_id=" + url.QueryEscape(r.ProjectID)
	asset := ReferenceAsset{Moderation: copyRunModeration(r.Moderation), ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, ContentType: r.Blob.ContentType, ByteSize: r.Blob.ByteSize, Width: r.Blob.Width, Height: r.Blob.Height, CreatedAt: r.CreatedAt, URL: base + "/content" + query, ThumbnailURL: base + "/thumbnail" + query}
	if r.Moderation != nil && r.Moderation.State != "ALLOWED" {
		asset.URL, asset.ThumbnailURL = "", ""
	}
	return asset
}
func referenceLimits() map[string]any {
	return map[string]any{"max_file_bytes": MaxReferenceBytes, "max_pixels": MaxReferencePixels, "max_dimension": MaxReferenceDimension, "max_reference_images": MaxReferenceFiles, "allowed_content_types": []string{"image/png", "image/jpeg", "image/webp"}, "workspace_max_bytes": maxWorkspaceReferenceBytes, "workspace_max_files": maxWorkspaceReferenceFiles}
}
func referenceName(raw string) string {
	name := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > 120 {
		runes = runes[:120]
	}
	if len(runes) == 0 || string(runes) == "." {
		return "参考图片"
	}
	return string(runes)
}
func referencePayloadHash(projectID, name string, blob ReferenceBlob) [32]byte {
	return payloadHash(struct {
		ProjectID, Name, SHA256 string
		ByteSize                int64
	}{projectID, name, blob.SHA256, blob.ByteSize})
}
func referenceStorageError(err error) error {
	switch {
	case errors.Is(err, ErrReferenceTooLarge):
		return failure(413, "REFERENCE_TOO_LARGE", "单张参考图片不得超过8 MiB")
	case errors.Is(err, ErrReferenceFormat):
		return failure(422, "REFERENCE_FORMAT_INVALID", "仅支持完整有效的PNG/JPEG/WebP图片，像素与尺寸须在限制内")
	case errors.Is(err, ErrReferenceBusy):
		return failure(429, "REFERENCE_UPLOAD_BUSY", "参考素材处理繁忙，请稍后重试原上传")
	case errors.Is(err, ErrReferenceStorageFull):
		return failure(507, "REFERENCE_STORAGE_FULL", "本地私有素材存储已满，请先处理存储容量")
	case errors.Is(err, ErrReferenceCorrupt):
		return failure(404, "REFERENCE_UNAVAILABLE", "参考图片不可用")
	default:
		return failure(503, "REFERENCE_STORAGE_UNAVAILABLE", "私有素材存储暂不可用，未确认受理时请保留原上传和幂等键")
	}
}

func (a *App) postReference(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	index := p.Workspace.ID + ":references:" + key
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		writeError(w, failure(415, "MULTIPART_REQUIRED", "请使用multipart/form-data上传单个file字段"))
		return
	}
	// A known key must reach content hashing even when the workspace is full;
	// an unknown key is refused before decoding/publishing more private blobs.
	a.mu.Lock()
	_, found, err := a.referenceIdempotencyLocked(index)
	if err == nil && !found {
		err = a.checkReferenceQuotaLocked(p.Workspace.ID, 1)
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxReferenceBytes+(64<<10))
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(2 * time.Minute)); err != nil {
		writeError(w, failure(503, "REFERENCE_READ_DEADLINE_UNAVAILABLE", "当前HTTP入口不支持受控上传读取，请使用本地Go服务入口"))
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, failure(400, "INVALID_REFERENCE_UPLOAD", "无效的上传表单"))
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		writeError(w, failure(400, "INVALID_REFERENCE_UPLOAD", "上传必须仅包含一个file文件字段"))
		return
	}
	name := referenceName(part.FileName())
	blob, err := a.references.PutImage(r.Context(), part)
	_ = part.Close()
	if err != nil {
		writeError(w, referenceStorageError(err))
		return
	}
	if extra, nextErr := reader.NextPart(); nextErr != io.EOF {
		if extra != nil {
			_ = extra.Close()
		}
		writeError(w, failure(400, "INVALID_REFERENCE_UPLOAD", "上传必须仅包含一个file文件字段"))
		return
	}
	if r.Context().Err() != nil {
		writeError(w, failure(503, "REFERENCE_UPLOAD_INTERRUPTED", "上传请求中断，请保留原文件和幂等键核对"))
		return
	}
	asset, replayed, err := a.acceptReferenceContext(r.Context(), p.Workspace.ID, p.Project.ID, name, index, blob, auditActorID(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, asset)
}

func (a *App) acceptReference(workspaceID, projectID, name, index string, blob ReferenceBlob, actorID string) (ReferenceAsset, bool, error) {
	return a.acceptReferenceContext(a.ctx, workspaceID, projectID, name, index, blob, actorID)
}

func (a *App) acceptReferenceContext(ctx context.Context, workspaceID, projectID, name, index string, blob ReferenceBlob, actorID string) (ReferenceAsset, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return ReferenceAsset{}, false, failure(503, "SHUTTING_DOWN", "服务正在停止")
	}
	project, ok := a.projects[projectID]
	if !ok || project.WorkspaceID != workspaceID {
		return ReferenceAsset{}, false, failure(404, "PROJECT_NOT_FOUND", "项目不存在")
	}
	hash := referencePayloadHash(projectID, name, blob)
	old, found, err := a.referenceIdempotencyLocked(index)
	if err != nil {
		return ReferenceAsset{}, false, err
	}
	if found {
		if old.Hash != hash {
			return ReferenceAsset{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "同一上传幂等键已用于其他文件或项目")
		}
		record, err := a.loadReferenceLocked(old.ID, workspaceID, projectID)
		if err != nil {
			return ReferenceAsset{}, false, err
		}
		return record.public(), true, nil
	}
	var policy *MediaModerationPolicy
	var summary *ModerationSummary
	if a.mediaModeration != nil {
		policy = a.mediaModeration.Snapshot()
		var checkErr error
		a.mu.Unlock()
		func() {
			defer a.mu.Lock()
			permit, permitErr := a.mediaModeration.acquireInputPermit(ctx, workspaceID)
			if permitErr != nil {
				checkErr = permitErr
				return
			}
			defer permit.Close()
			file, openErr := a.references.OpenVerified(ctx, blob)
			if openErr != nil {
				checkErr = referenceStorageError(openErr)
				return
			}
			defer file.Close()
			decision := permit.Check(ctx, policy, "input", MediaModerationContent{Kind: "image", ContentType: blob.ContentType, ByteSize: blob.ByteSize, SHA256: blob.SHA256}, file)
			if decision.State != "ALLOWED" {
				checkErr = mediaModerationInputError(decision)
			}
		}()
		// Original acceptance wins over a later policy result or full capacity.
		old, found, lookupErr := a.referenceIdempotencyLocked(index)
		if lookupErr != nil {
			return ReferenceAsset{}, false, lookupErr
		}
		if found {
			if old.Hash != hash {
				return ReferenceAsset{}, false, failure(409, "IDEMPOTENCY_CONFLICT", "同一上传幂等键已用于其他文件或项目")
			}
			record, err := a.loadReferenceLocked(old.ID, workspaceID, projectID)
			if err != nil {
				return ReferenceAsset{}, false, err
			}
			return record.public(), true, nil
		}
		if checkErr != nil {
			if errors.Is(checkErr, context.Canceled) || errors.Is(checkErr, context.DeadlineExceeded) {
				return ReferenceAsset{}, false, failure(503, "MEDIA_MODERATION_UNAVAILABLE", "本次素材审核等待已中断，尚未受理；请保留原文件和幂等键核对")
			}
			return ReferenceAsset{}, false, checkErr
		}
		if a.ctx.Err() != nil || ctx.Err() != nil {
			return ReferenceAsset{}, false, failure(503, "MEDIA_MODERATION_UNAVAILABLE", "本次素材尚未受理，请保留原文件核对")
		}
		project, ok = a.projects[projectID]
		if !ok || project.WorkspaceID != workspaceID {
			return ReferenceAsset{}, false, failure(404, "PROJECT_NOT_FOUND", "项目不存在")
		}
		summary = InitialMediaModerationSummary(policy, "input")
		summary.State, summary.Version, summary.ReasonCode = "ALLOWED", 2, "allowed"
	}
	stored := blob.ByteSize
	if blob.Thumbnail != nil {
		stored += blob.Thumbnail.ByteSize
	}
	if err := a.checkReferenceQuotaLocked(workspaceID, stored); err != nil {
		return ReferenceAsset{}, false, err
	}
	if err := a.checkMemoryLibraryCapacityLocked(1); err != nil {
		return ReferenceAsset{}, false, err
	}
	record := referenceRecord{ID: newID("ref"), WorkspaceID: workspaceID, ProjectID: projectID, Name: name, Blob: blob, StoredBytes: stored, CreatedAt: time.Now().UTC()}
	record.ModerationPolicy, record.Moderation = policy, summary
	audit := newAuditEvent(workspaceID, projectID, actorID, "reference.uploaded", "reference", record.ID, AuditDetails{Kind: "image"})
	if a.store != nil {
		if err := a.store.createReference(record, index, hash, audit); err != nil {
			return ReferenceAsset{}, false, err
		}
	} else {
		a.referenceItems[record.ID] = record
		a.referenceIdem[index] = idemRecord{Hash: hash, ID: record.ID}
		a.registerMemoryLibraryLocked(libraryFromReference(record))
		a.appendMemoryAuditLocked(audit)
	}
	return record.public(), false, nil
}
func (a *App) referenceIdempotencyLocked(index string) (idemRecord, bool, error) {
	if a.store == nil {
		old, found := a.referenceIdem[index]
		return old, found, nil
	}
	var old idemRecord
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var hash []byte
		err := conn.QueryRow(ctx, "SELECT payload_hash,reference_id FROM studio_reference_idempotency WHERE scope_key=$1", index).Scan(&hash, &old.ID)
		copy(old.Hash[:], hash)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return old, false, nil
	}
	return old, err == nil, err
}
func (a *App) checkReferenceQuotaLocked(workspaceID string, additionalBytes int64) error {
	files, bytes := 0, int64(0)
	if a.store != nil {
		err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			return conn.QueryRow(ctx, "SELECT count(*),coalesce(sum(stored_bytes),0) FROM studio_references WHERE workspace_id=$1", workspaceID).Scan(&files, &bytes)
		})
		if err != nil {
			return err
		}
	} else {
		for _, r := range a.referenceItems {
			if r.WorkspaceID == workspaceID {
				files++
				bytes += r.StoredBytes
			}
		}
	}
	if files >= maxWorkspaceReferenceFiles || additionalBytes > maxWorkspaceReferenceBytes-bytes {
		return failure(429, "REFERENCE_WORKSPACE_LIMIT", "工作区参考素材已达100张或128 MiB上限；可以复用已有素材")
	}
	return nil
}
func (a *App) loadReferenceLocked(id, workspaceID, projectID string) (referenceRecord, error) {
	// Reject malformed external IDs before pgx encoding. In particular a JSON
	// NUL must not be mistaken for a database failure and trip the writer fuse.
	if !referenceAssetID.MatchString(id) {
		return referenceRecord{}, failure(404, "REFERENCE_NOT_FOUND", "参考素材不存在或不属于当前项目")
	}
	if a.store == nil {
		record, ok := a.referenceItems[id]
		if !ok || record.WorkspaceID != workspaceID || record.ProjectID != projectID {
			return referenceRecord{}, failure(404, "REFERENCE_NOT_FOUND", "参考素材不存在或不属于当前项目")
		}
		return record, nil
	}
	var record referenceRecord
	err := a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
		var blob []byte
		err := conn.QueryRow(ctx, "SELECT id,workspace_id,project_id,name,blob,stored_bytes,created_at FROM studio_references WHERE id=$1 AND workspace_id=$2 AND project_id=$3", id, workspaceID, projectID).Scan(&record.ID, &record.WorkspaceID, &record.ProjectID, &record.Name, &blob, &record.StoredBytes, &record.CreatedAt)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(blob, &record.Blob); err != nil {
			return err
		}
		record.ModerationPolicy, record.Moderation, err = loadReferenceMediaModerationSQL(ctx, conn, record.ID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "REFERENCE_NOT_FOUND", "参考素材不存在或不属于当前项目")
	}
	return record, err
}
func (s *postgresStore) createReference(record referenceRecord, index string, hash [32]byte, audit AuditEvent) error {
	return s.saveWithAudit(func(ctx context.Context, tx pgx.Tx) error {
		blob, _ := json.Marshal(record.Blob)
		if _, err := tx.Exec(ctx, "INSERT INTO studio_references(id,workspace_id,project_id,name,blob,stored_bytes,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)", record.ID, record.WorkspaceID, record.ProjectID, record.Name, blob, record.StoredBytes, record.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO studio_reference_idempotency(scope_key,payload_hash,reference_id) VALUES($1,$2,$3)", index, hash[:], record.ID)
		if err != nil {
			return err
		}
		if record.Moderation != nil {
			if err := insertReferenceMediaModerationSQL(ctx, tx, record.ID, record.WorkspaceID, record.ProjectID, record.ModerationPolicy, record.Moderation); err != nil {
				return err
			}
		}
		return insertLibrarySQL(ctx, tx, libraryFromReference(record))
	}, []AuditEvent{audit})
}
func (a *App) listReferences(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	limit, offset := 100, 0
	for name, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || name == "limit" && (value < 1 || value > 100) || name == "offset" && value > 100000 {
				writeError(w, failure(400, "INVALID_PAGINATION", "分页参数无效"))
				return
			}
			*target = value
		}
	}
	items := []referenceRecord{}
	a.mu.Lock()
	var err error
	if a.store != nil {
		err = a.store.do(func(ctx context.Context, conn *pgx.Conn) error {
			rows, err := conn.Query(ctx, "SELECT id,workspace_id,project_id,name,blob,stored_bytes,created_at,"+referenceModerationProjection+" FROM studio_references WHERE workspace_id=$1 AND project_id=$2 AND EXISTS(SELECT 1 FROM studio_library_entries l WHERE l.reference_id=studio_references.id AND l.state='active') ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4", p.Workspace.ID, p.Project.ID, limit+1, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var record referenceRecord
				var blob, moderation []byte
				if err := rows.Scan(&record.ID, &record.WorkspaceID, &record.ProjectID, &record.Name, &blob, &record.StoredBytes, &record.CreatedAt, &moderation); err != nil {
					return err
				}
				if err := json.Unmarshal(blob, &record.Blob); err != nil {
					return err
				}
				if len(moderation) > 0 {
					if err := json.Unmarshal(moderation, &record.Moderation); err != nil {
						return err
					}
				}
				items = append(items, record)
			}
			return rows.Err()
		})
	} else {
		for _, record := range a.referenceItems {
			if record.WorkspaceID == p.Workspace.ID && record.ProjectID == p.Project.ID {
				if entry, found := a.library[libraryID("reference", record.ID, "")]; found && entry.State == "active" {
					items = append(items, record)
				}
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID > items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
		if offset >= len(items) {
			items = nil
		} else {
			items = items[offset:min(offset+limit+1, len(items))]
		}
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	result := make([]ReferenceAsset, 0, len(items))
	for _, record := range items {
		result = append(result, record.public())
	}
	writeJSON(w, 200, map[string]any{"items": result, "limit": limit, "offset": offset, "has_more": hasMore})
}
func (a *App) serveReference(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	a.mu.Lock()
	record, err := a.loadReferenceLocked(r.PathValue("id"), p.Workspace.ID, p.Project.ID)
	if err == nil {
		err = a.referenceActiveLocked(record.ID, p.Workspace.ID, p.Project.ID)
	}
	if err == nil && record.Moderation != nil && record.Moderation.State != "ALLOWED" {
		err = failure(404, "REFERENCE_UNAVAILABLE", "素材尚未开放")
	}
	a.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	blob := record.Blob
	if strings.HasSuffix(r.URL.Path, "/thumbnail") {
		if blob.Thumbnail == nil {
			writeError(w, failure(404, "REFERENCE_UNAVAILABLE", "素材缩略图不存在"))
			return
		}
		blob = *blob.Thumbnail
	}
	file, err := a.references.OpenVerified(r.Context(), blob)
	if err != nil {
		writeError(w, referenceStorageError(err))
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", blob.ContentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[blob.ContentType]
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s%s"`, record.ID, ext))
	http.ServeContent(w, r, record.ID+ext, record.CreatedAt, file)
}

func (a *App) resolveReferencesLocked(workspaceID, projectID string, ids []string) ([]referenceRecord, error) {
	if len(ids) < 1 || len(ids) > MaxReferenceFiles {
		return nil, failure(422, "INVALID_REFERENCES", "图片编辑需要1至4张参考素材")
	}
	seen := map[string]bool{}
	result := make([]referenceRecord, 0, len(ids))
	for _, id := range ids {
		if seen[id] || !referenceAssetID.MatchString(id) {
			return nil, failure(422, "INVALID_REFERENCES", "参考素材不能重复或包含无效ID")
		}
		seen[id] = true
		record, err := a.loadReferenceLocked(id, workspaceID, projectID)
		if err != nil {
			return nil, err
		}
		if err := a.referenceActiveLocked(id, workspaceID, projectID); err != nil {
			return nil, err
		}
		if record.Moderation != nil && record.Moderation.State != "ALLOWED" {
			return nil, failure(422, "MEDIA_MODERATION_INPUT_REVIEW", "参考素材尚未开放，未创建本次生成")
		}
		result = append(result, record)
	}
	return result, nil
}
func (a *App) prepareImageEdit(ctx context.Context, input TaskInput, upstreamModel string, refs []referenceRecord) (*MediaSubmission, error) {
	select {
	case a.editPrepareSlots <- struct{}{}:
		defer func() { <-a.editPrepareSlots }()
	default:
		return nil, failure(429, "REFERENCE_PREPARATION_BUSY", "图片编辑准备繁忙，请稍后重试原请求")
	}
	sources := make([]MediaEditSource, 0, len(refs))
	for _, record := range refs {
		blob := record.Blob
		sources = append(sources, MediaEditSource{ContentType: blob.ContentType, ByteSize: blob.ByteSize, SHA256: blob.SHA256, Open: func() (io.ReadCloser, error) { return a.references.OpenVerified(ctx, blob) }})
	}
	if provider, ok := a.media.(interface {
		PrepareEditModel(context.Context, TaskInput, string, []MediaEditSource) (MediaSubmission, error)
	}); ok {
		submission, err := provider.PrepareEditModel(ctx, input, upstreamModel, sources)
		if err != nil {
			var providerError *MediaProviderError
			if errors.As(err, &providerError) {
				switch providerError.Code {
				case "REFERENCE_PREPARATION_BUSY":
					return nil, failure(429, providerError.Code, "图片编辑准备繁忙，请稍后重试原请求")
				case "REFERENCE_STORAGE_FULL":
					return nil, failure(507, providerError.Code, "本地私有素材存储已满")
				}
			}
			return nil, failure(422, "MEDIA_REFERENCE_UNAVAILABLE", "参考素材不可用，本次尚未提交生成")
		}
		return &submission, nil
	}
	if a.config.Mode != "demo" {
		return nil, failure(501, "IMAGE_EDIT_UNAVAILABLE", "当前供应商未配置图片编辑能力")
	}
	for _, source := range sources {
		file, err := source.Open()
		if err != nil {
			return nil, referenceStorageError(err)
		}
		_ = file.Close()
	}
	return nil, nil
}
