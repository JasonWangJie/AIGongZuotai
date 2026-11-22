package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const comicVersionLimit = 128

type ComicCard struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type ComicPanel struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	SceneID        string   `json:"scene_id"`
	CharacterIDs   []string `json:"character_ids"`
	Action         string   `json:"action"`
	Dialogue       string   `json:"dialogue"`
	AssetID        string   `json:"asset_id,omitempty"`
	AssetRevision  uint64   `json:"asset_revision,omitempty"`
	ContentVersion string   `json:"content_version,omitempty"`
	AssetAvailable *bool    `json:"asset_available,omitempty"`
}
type ComicDocument struct {
	Title      string       `json:"title"`
	Story      string       `json:"story"`
	Characters []ComicCard  `json:"characters"`
	Scenes     []ComicCard  `json:"scenes"`
	Panels     []ComicPanel `json:"panels"`
}
type ComicSaveInput struct {
	ExpectedVersion *uint64       `json:"expected_version"`
	Document        ComicDocument `json:"document"`
}
type ComicVersion struct {
	ProjectID string        `json:"project_id"`
	Version   uint64        `json:"version"`
	Document  ComicDocument `json:"document"`
	Confirmed bool          `json:"confirmed"`
	CreatedAt time.Time     `json:"created_at"`
}
type ComicSummary struct {
	ProjectID  string    `json:"project_id"`
	Version    uint64    `json:"version"`
	Title      string    `json:"title"`
	PanelCount int       `json:"panel_count"`
	Confirmed  bool      `json:"confirmed"`
	CreatedAt  time.Time `json:"created_at"`
}
type ComicHistoryPage struct {
	Items             []ComicSummary `json:"items"`
	NextBeforeVersion *uint64        `json:"next_before_version"`
}

func comicMissing() error {
	return failure(404, "COMIC_NOT_FOUND", "未找到本项目的分镜本、版本、镜头或原键收据")
}
func comicInvalid() error {
	return failure(422, "INVALID_COMIC", "请核对分镜本的角色、场景、镜头、图片修订和字段长度")
}
func comicConflict() error {
	return failure(409, "COMIC_VERSION_CONFLICT", "分镜本版本已变化，请读取当前版本")
}
func comicUnavailable() error {
	return failure(409, "COMIC_ASSET_UNAVAILABLE", "镜头原图不可见、已变动或修订已过期")
}
func comicReadError(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return comicMissing()
	}
	return e
}

func comicCardIDs(cards []ComicCard) (map[string]bool, error) {
	if len(cards) < 1 || len(cards) > 16 {
		return nil, comicInvalid()
	}
	ids := make(map[string]bool, len(cards))
	for _, c := range cards {
		if !presentationSlideID.MatchString(c.ID) || ids[c.ID] || !validPresentationText(c.Name, 80, true) || !validPresentationText(c.Description, 1000, false) {
			return nil, comicInvalid()
		}
		ids[c.ID] = true
	}
	return ids, nil
}
func validateComic(in ComicSaveInput) error {
	d := in.Document
	if in.ExpectedVersion == nil || *in.ExpectedVersion > comicVersionLimit || !validPresentationText(d.Title, 120, true) ||
		!validPresentationText(d.Story, 4000, false) || strings.TrimSpace(d.Story) == "" || len(d.Panels) < 1 || len(d.Panels) > 12 {
		return comicInvalid()
	}
	roles, e := comicCardIDs(d.Characters)
	if e != nil {
		return e
	}
	scenes, e := comicCardIDs(d.Scenes)
	if e != nil {
		return e
	}
	panels := map[string]bool{}
	images := 0
	for _, p := range d.Panels {
		if !presentationSlideID.MatchString(p.ID) || panels[p.ID] || !validPresentationText(p.Title, 120, true) || !scenes[p.SceneID] ||
			p.CharacterIDs == nil || len(p.CharacterIDs) > 8 || !validPresentationText(p.Action, 1000, false) || strings.TrimSpace(p.Action) == "" || !validPresentationText(p.Dialogue, 1000, false) ||
			p.ContentVersion != "" || p.AssetAvailable != nil {
			return comicInvalid()
		}
		panels[p.ID] = true
		seen := map[string]bool{}
		for _, id := range p.CharacterIDs {
			if !roles[id] || seen[id] {
				return comicInvalid()
			}
			seen[id] = true
		}
		if p.AssetID == "" {
			if p.AssetRevision != 0 {
				return comicInvalid()
			}
			continue
		}
		if !libraryIDPattern.MatchString(p.AssetID) || p.AssetRevision < 1 || p.AssetRevision > libraryRevisionLimit {
			return comicInvalid()
		}
		images++
	}
	if images > 4 {
		return failure(422, "COMIC_IMAGE_LIMIT", "每版最多四个图片镜头")
	}
	encoded, e := json.Marshal(d)
	if e != nil {
		return comicInvalid()
	}
	if len(encoded) > presentationMaxDocumentBytes {
		return failure(413, "COMIC_TOO_LARGE", "分镜本正文的 JSON 编码超过 256 KiB")
	}
	return nil
}

func (a *App) comicTx(p *principal, project string, save bool, fn func(context.Context, pgx.Tx) error) error {
	err := a.presentationTx(p, project, save, fn)
	var op *operationError
	if errors.As(err, &op) {
		switch op.code {
		case "PRESENTATION_NOT_FOUND":
			return comicMissing()
		case "PRESENTATION_UNAVAILABLE":
			return failure(503, "COMIC_UNAVAILABLE", "分镜本需要持久账户模式")
		}
	}
	return err
}

func comicReceiptSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, actor, operation, key string) (version uint64, digest []byte, err error) {
	k := sha256.Sum256([]byte(key))
	err = tx.QueryRow(ctx, `SELECT version,request_hash FROM studio_comic_receipts WHERE project_id=$1 AND workspace_id=$2 AND actor_id=$3 AND operation=$4 AND key_hash=$5`,
		scope.ProjectID, scope.WorkspaceID, actor, operation, k[:]).Scan(&version, &digest)
	return
}

func (a *App) comicWrite(p *principal, project, operation, key string, digest [32]byte, change func(context.Context, pgx.Tx, subscriptionScope, uint64) (uint64, error)) (out ComicVersion, replayed bool, err error) {
	if !workflowKey.MatchString(key) {
		return out, false, failure(422, "INVALID_IDEMPOTENCY_KEY", "请提供有效操作原键")
	}
	err = a.comicTx(p, project, true, func(ctx context.Context, tx pgx.Tx) error {
		scope := subscriptionScope{p.Workspace.ID, project}
		v, hash, e := comicReceiptSQL(ctx, tx, scope, p.User.ID, operation, key)
		if e == nil {
			if !bytes.Equal(hash, digest[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "原键已用于不同的分镜操作")
			}
			replayed = true
		} else {
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			var head uint64
			e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT version FROM studio_comics WHERE project_id=$1 AND workspace_id=$2),0)`, project, scope.WorkspaceID).Scan(&head)
			if e != nil {
				return e
			}
			v, e = change(ctx, tx, scope, head)
			if e != nil {
				return e
			}
			k := sha256.Sum256([]byte(key))
			_, e = tx.Exec(ctx, `INSERT INTO studio_comic_receipts(project_id,workspace_id,actor_id,operation,key_hash,request_hash,version) VALUES($1,$2,$3,$4,$5,$6,$7)`, project, scope.WorkspaceID, p.User.ID, operation, k[:], digest[:], v)
			if e != nil {
				return e
			}
		}
		out, _, e = readComicSQL(ctx, tx, scope, v, "", false)
		return e
	})
	if err != nil {
		return ComicVersion{}, false, err
	}
	return
}

func (a *App) saveComic(p *principal, project string, in ComicSaveInput, key string) (ComicVersion, bool, error) {
	return a.comicWrite(p, project, "save", key, payloadHash(in), func(ctx context.Context, tx pgx.Tx, scope subscriptionScope, head uint64) (uint64, error) {
		if e := validateComic(in); e != nil {
			return 0, e
		}
		if *in.ExpectedVersion != head {
			return 0, comicConflict()
		}
		if head >= comicVersionLimit {
			return 0, failure(429, "COMIC_VERSION_LIMIT", "分镜本已达到 128 个版本")
		}
		doc := in.Document
		doc.Panels = append([]ComicPanel(nil), doc.Panels...)
		frozen, e := freezeComicAssetsSQL(ctx, tx, scope, &doc)
		if e != nil {
			return 0, e
		}
		v := head + 1
		if head == 0 {
			_, e = tx.Exec(ctx, `INSERT INTO studio_comics(project_id,workspace_id,version) VALUES($1,$2,1)`, project, scope.WorkspaceID)
		} else {
			_, e = tx.Exec(ctx, `UPDATE studio_comics SET version=$2 WHERE project_id=$1`, project, v)
		}
		if e != nil {
			return 0, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO studio_comic_versions(project_id,workspace_id,version,created_by,document) VALUES($1,$2,$3,$4,$5)`, project, scope.WorkspaceID, v, p.User.ID, doc)
		if e != nil {
			return 0, e
		}
		for _, panel := range doc.Panels {
			if panel.AssetID == "" {
				continue
			}
			_, e = tx.Exec(ctx, `INSERT INTO studio_comic_assets(project_id,workspace_id,version,panel_id,asset_id,asset_revision,content_version,original)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, project, scope.WorkspaceID, v, panel.ID, panel.AssetID, panel.AssetRevision, panel.ContentVersion, frozen[panel.ID])
			if e != nil {
				return 0, e
			}
		}
		return v, nil
	})
}

func (a *App) confirmComic(p *principal, project string, v uint64, key string) (ComicVersion, bool, error) {
	return a.comicWrite(p, project, "confirm", key, payloadHash(v), func(ctx context.Context, tx pgx.Tx, scope subscriptionScope, head uint64) (uint64, error) {
		if v == 0 || v > comicVersionLimit {
			return 0, comicMissing()
		}
		if head != v {
			return 0, comicConflict()
		}
		var author string
		var confirmed bool
		e := tx.QueryRow(ctx, `SELECT created_by,EXISTS(SELECT 1 FROM studio_comic_receipts WHERE project_id=$1 AND version=$2 AND operation='confirm')
		 FROM studio_comic_versions WHERE project_id=$1 AND version=$2 AND workspace_id=$3`, project, v, scope.WorkspaceID).Scan(&author, &confirmed)
		if e != nil {
			return 0, comicReadError(e)
		}
		if p.User.ID != author {
			return 0, failure(403, "COMIC_AUTHOR_REQUIRED", "此版本须由原作者确认")
		}
		if confirmed {
			return 0, failure(409, "COMIC_ALREADY_CONFIRMED", "此版本已由作者确认")
		}
		_, _, e = readComicSQL(ctx, tx, scope, v, "", true)
		return v, e
	})
}
