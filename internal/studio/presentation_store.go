package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const presentationVersionLimit = 128

// Normalized client JSON leaves 64 KiB below the storage guard for the
// maximum four frozen hashes and JSONB's whitespace between object fields.
const presentationMaxDocumentBytes = 256 << 10
const presentationMaxImageBytes = 8 << 20
const presentationMaxImageTotal = 32 << 20

var presentationSlideID = regexp.MustCompile("^[A-Za-z0-9_-]{1,48}$")

type PresentationDocument struct {
	Title  string              `json:"title"`
	Theme  string              `json:"theme"`
	Slides []PresentationSlide `json:"slides"`
}
type PresentationSlide struct {
	ID             string `json:"id"`
	Layout         string `json:"layout"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Notes          string `json:"notes"`
	AssetID        string `json:"asset_id,omitempty"`
	AssetRevision  uint64 `json:"asset_revision,omitempty"`
	ContentVersion string `json:"content_version,omitempty"`
	AssetAvailable *bool  `json:"asset_available,omitempty"`
}
type PresentationSaveInput struct {
	PresentationID  string               `json:"presentation_id,omitempty"`
	ExpectedVersion *uint64              `json:"expected_version"`
	Document        PresentationDocument `json:"document"`
}
type PresentationVersion struct {
	ID        string               `json:"id"`
	ProjectID string               `json:"project_id"`
	Version   uint64               `json:"version"`
	CreatedAt time.Time            `json:"created_at"`
	Document  PresentationDocument `json:"document"`
}
type PresentationSummary struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Version    uint64    `json:"version"`
	Title      string    `json:"title"`
	SlideCount int       `json:"slide_count"`
	CreatedAt  time.Time `json:"created_at"`
}
type PresentationPage struct {
	Items      []PresentationSummary `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}
type PresentationHistoryPage struct {
	Items             []PresentationSummary `json:"items"`
	NextBeforeVersion *uint64               `json:"next_before_version"`
}

func presentationMissing() error {
	return failure(404, "PRESENTATION_NOT_FOUND", "该项目中没有此文稿、版本或回执")
}
func presentationAssetUnavailable() error {
	return failure(409, "PRESENTATION_ASSET_UNAVAILABLE", "原稿包含当前不可访问、内容已变化或修订不符的图片")
}
func presentationReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return presentationMissing()
	}
	return err
}
func validPresentationText(s string, limit int, title bool) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > limit || title && strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r == 0xfffe || r == 0xffff || unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && (title || r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	return true
}
func validatePresentation(in PresentationSaveInput) error {
	invalid := func() error {
		return failure(422, "INVALID_PRESENTATION", "请核对文稿版本、主题、布局、文字长度和图片修订")
	}
	d := in.Document
	if in.ExpectedVersion == nil || *in.ExpectedVersion > presentationVersionLimit || in.PresentationID == "" && *in.ExpectedVersion != 0 || in.PresentationID != "" && (!validOperationsID(in.PresentationID, "pres") || *in.ExpectedVersion == 0) {
		return invalid()
	}
	if !validPresentationText(d.Title, 120, true) || (d.Theme != "paper" && d.Theme != "midnight" && d.Theme != "mint") || len(d.Slides) < 1 || len(d.Slides) > 30 {
		return invalid()
	}
	ids := map[string]bool{}
	images := 0
	for _, s := range d.Slides {
		if !presentationSlideID.MatchString(s.ID) || ids[s.ID] || !validPresentationText(s.Title, 120, true) || !validPresentationText(s.Body, 4000, false) || !validPresentationText(s.Notes, 2000, false) || s.AssetAvailable != nil || s.ContentVersion != "" {
			return invalid()
		}
		ids[s.ID] = true
		switch s.Layout {
		case "title", "content":
			if s.AssetID != "" || s.AssetRevision != 0 {
				return invalid()
			}
		case "image":
			if !libraryIDPattern.MatchString(s.AssetID) || s.AssetRevision == 0 || s.AssetRevision > libraryRevisionLimit {
				return invalid()
			}
			images++
			if images > 4 {
				return failure(422, "PRESENTATION_IMAGE_LIMIT", "每份文稿最多四个图片页")
			}
		default:
			return invalid()
		}
	}
	raw, e := json.Marshal(d)
	if e != nil {
		return invalid()
	}
	if len(raw) > presentationMaxDocumentBytes {
		return failure(413, "PRESENTATION_TOO_LARGE", "文稿正文的 JSON 编码超过 256 KiB")
	}
	return nil
}
func (a *App) presentationTx(p *principal, project string, save bool, fn func(context.Context, pgx.Tx) error) error {
	if a.store == nil || a.config.AuthMode != "required" {
		return failure(503, "PRESENTATION_UNAVAILABLE", "文稿需要持久账户模式")
	}
	if p == nil || p.User == nil {
		return failure(401, "AUTH_REQUIRED", "请先登录")
	}
	if !validOperationsID(project, "prj") {
		return presentationMissing()
	}
	return a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
			if e := checkOrdinaryAccountSQL(ctx, tx, p); e != nil {
				return e
			}
			q := "SELECT id FROM studio_projects WHERE id=$1 AND workspace_id=$2"
			if save {
				q += " FOR NO KEY UPDATE"
			}
			var id string
			if e := tx.QueryRow(ctx, q, project, p.Workspace.ID).Scan(&id); e != nil {
				return presentationReadError(e)
			}
			return fn(ctx, tx)
		})
	})
}
func presentationVersionSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, version uint64) (v PresentationVersion, err error) {
	err = tx.QueryRow(ctx, `SELECT presentation_id,project_id,version,created_at,document FROM studio_presentation_versions WHERE presentation_id=$1 AND workspace_id=$2 AND project_id=$3 AND version=$4`, id, scope.WorkspaceID, scope.ProjectID, version).Scan(&v.ID, &v.ProjectID, &v.Version, &v.CreatedAt, &v.Document)
	return v, presentationReadError(err)
}
func presentationKeySQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, key string) (id string, version uint64, hash []byte, err error) {
	k := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT presentation_id,version,request_hash FROM studio_presentation_keys WHERE workspace_id=$1 AND project_id=$2 AND actor_id=$3 AND key_hash=$4`, scope.WorkspaceID, scope.ProjectID, actor, k[:]).Scan(&id, &version, &hash)
	return
}
func (a *App) savePresentation(p *principal, project string, in PresentationSaveInput, key string) (out PresentationVersion, replay bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效的文稿保存原键")
	}
	err = a.presentationTx(p, project, true, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		h := payloadHash(in)
		id, version, saved, e := presentationKeySQL(ctx, tx, scope, p.User.ID, key)
		if e == nil {
			if !bytes.Equal(saved, h[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键已用于另一份文稿修改")
			}
			replay = true
			out, e = presentationVersionSQL(ctx, tx, scope, id, version)
			if e != nil {
				return e
			}
			return projectPresentationAssets(ctx, tx, scope, &out.Document, false, false)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e = validatePresentation(in); e != nil {
			return e
		}
		id = in.PresentationID
		if id == "" {
			if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x505245534e54)); e != nil {
				return e
			}
			var own, total int
			e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE project_id=$1),count(*) FROM (SELECT project_id FROM studio_presentations LIMIT 4097) d`, project).Scan(&own, &total)
			if e != nil {
				return e
			}
			if own >= 64 || total >= 4096 {
				return failure(429, "PRESENTATION_LIMIT", "文稿数量已达上限")
			}
			id = newID("pres")
		} else {
			e = tx.QueryRow(ctx, `SELECT version FROM studio_presentations WHERE id=$1 AND project_id=$2 AND workspace_id=$3 FOR UPDATE`, id, project, scope.WorkspaceID).Scan(&version)
			if e != nil {
				return presentationReadError(e)
			}
			if version != *in.ExpectedVersion {
				return failure(409, "PRESENTATION_VERSION_CONFLICT", "文稿已有新版本，请读取后再保存")
			}
			if version >= presentationVersionLimit {
				return failure(429, "PRESENTATION_VERSION_LIMIT", "文稿已达版本上限")
			}
		}
		d := in.Document
		d.Slides = append([]PresentationSlide(nil), d.Slides...)
		if e = projectPresentationAssets(ctx, tx, scope, &d, true, true); e != nil {
			return e
		}
		version = *in.ExpectedVersion + 1
		if version == 1 {
			_, e = tx.Exec(ctx, `INSERT INTO studio_presentations(id,workspace_id,project_id,version,title,slide_count) VALUES($1,$2,$3,1,$4,$5)`, id, scope.WorkspaceID, project, d.Title, len(d.Slides))
		} else {
			_, e = tx.Exec(ctx, `UPDATE studio_presentations SET version=$2,title=$3,slide_count=$4,updated_at=clock_timestamp() WHERE id=$1`, id, version, d.Title, len(d.Slides))
		}
		if e != nil {
			return e
		}
		out = PresentationVersion{ID: id, ProjectID: project, Version: version, Document: d}
		e = tx.QueryRow(ctx, `INSERT INTO studio_presentation_versions(presentation_id,workspace_id,project_id,version,created_by,document) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at`, id, scope.WorkspaceID, project, version, p.User.ID, d).Scan(&out.CreatedAt)
		if e != nil {
			return e
		}
		for _, sl := range d.Slides {
			if sl.AssetID == "" {
				continue
			}
			if _, e = tx.Exec(ctx, `INSERT INTO studio_presentation_assets(presentation_id,version,project_id,workspace_id,slide_id,asset_id,asset_revision,content_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, version, project, scope.WorkspaceID, sl.ID, sl.AssetID, sl.AssetRevision, sl.ContentVersion); e != nil {
				return e
			}
		}
		k := sha256.Sum256([]byte(key))
		_, e = tx.Exec(ctx, `INSERT INTO studio_presentation_keys(workspace_id,project_id,actor_id,key_hash,request_hash,presentation_id,version) VALUES($1,$2,$3,$4,$5,$6,$7)`, scope.WorkspaceID, project, p.User.ID, k[:], h[:], id, version)
		for i := range out.Document.Slides {
			if out.Document.Slides[i].AssetID != "" {
				yes := true
				out.Document.Slides[i].AssetAvailable = &yes
			}
		}
		return e
	})
	if err != nil {
		return PresentationVersion{}, false, err
	}
	return
}
func (a *App) presentationReceipt(p *principal, project, key string) (out PresentationVersion, err error) {
	err = a.presentationTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		id, version, _, e := presentationKeySQL(ctx, tx, scope, p.User.ID, key)
		if e != nil {
			return presentationReadError(e)
		}
		out, e = presentationVersionSQL(ctx, tx, scope, id, version)
		if e != nil {
			return e
		}
		return projectPresentationAssets(ctx, tx, scope, &out.Document, false, false)
	})
	return
}
func (a *App) presentationVersion(p *principal, project, id string, version uint64, requireAssets bool) (out PresentationVersion, err error) {
	if !validOperationsID(id, "pres") || version == 0 || version > presentationVersionLimit {
		return out, presentationMissing()
	}
	err = a.presentationTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		var e error
		out, e = presentationVersionSQL(ctx, tx, scope, id, version)
		if e != nil {
			return e
		}
		return projectPresentationAssets(ctx, tx, scope, &out.Document, false, requireAssets)
	})
	return
}

type presentationCursor struct {
	ID, Project, Actor string
	Created            time.Time
}

func presentationPageLimit(n int) (int, error) {
	if n == 0 {
		return 20, nil
	}
	if n < 1 || n > 50 {
		return 0, failure(400, "INVALID_LIMIT", "页大小应为 1 至 50")
	}
	return n, nil
}
func (a *App) listPresentations(p *principal, project, cursor string, limit int) (out PresentationPage, err error) {
	out.Items = []PresentationSummary{}
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return
	}
	err = a.presentationTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		c := presentationCursor{Project: project, Actor: p.User.ID}
		if cursor != "" {
			if len(cursor) > 1024 {
				return failure(400, "INVALID_CURSOR", "文稿分页游标无效")
			}
			b, e := base64.RawURLEncoding.DecodeString(cursor)
			if e != nil || json.Unmarshal(b, &c) != nil || c.Project != project || c.Actor != p.User.ID || !validOperationsID(c.ID, "pres") || c.Created.Year() < 2000 || c.Created.Year() > 9999 {
				return failure(400, "INVALID_CURSOR", "文稿分页游标无效")
			}
		}
		rows, e := tx.Query(ctx, `SELECT id,project_id,version,title,slide_count,created_at FROM studio_presentations WHERE workspace_id=$1 AND project_id=$2 AND ($3::timestamptz='0001-01-01T00:00:00Z' OR (created_at,id)<($3,$4)) ORDER BY created_at DESC,id DESC LIMIT $5`, p.Workspace.ID, project, c.Created, c.ID, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[PresentationSummary])
		if e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			c.ID, c.Created = last.ID, last.CreatedAt
			b, _ := json.Marshal(c)
			n := base64.RawURLEncoding.EncodeToString(b)
			out.NextCursor = &n
		}
		return nil
	})
	return
}
func (a *App) presentationHistory(p *principal, project, id string, before uint64, limit int) (out PresentationHistoryPage, err error) {
	out.Items = []PresentationSummary{}
	limit, err = presentationPageLimit(limit)
	if err != nil {
		return
	}
	if !validOperationsID(id, "pres") || before > presentationVersionLimit {
		return out, presentationMissing()
	}
	err = a.presentationTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_presentations WHERE id=$1 AND workspace_id=$2 AND project_id=$3)`, id, p.Workspace.ID, project).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return presentationMissing()
		}
		rows, e := tx.Query(ctx, `SELECT presentation_id,project_id,version,document->>'title',jsonb_array_length(document->'slides'),created_at FROM studio_presentation_versions WHERE presentation_id=$1 AND workspace_id=$2 AND project_id=$3 AND ($4::bigint=0 OR version<$4) ORDER BY version DESC LIMIT $5`, id, p.Workspace.ID, project, before, limit+1)
		if e != nil {
			return e
		}
		out.Items, e = pgx.CollectRows(rows, pgx.RowToStructByPos[PresentationSummary])
		if e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			v := out.Items[limit-1].Version
			out.NextBeforeVersion = &v
		}
		return nil
	})
	return
}
