package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func draftReceiptSQL(ctx context.Context, tx pgx.Tx, workspace, project, actor, key string, member bool) ([]byte, uint64, error) {
	var digest []byte
	var revision uint64
	var err error
	if member {
		k := sha256.Sum256([]byte(key))
		err = tx.QueryRow(ctx, `SELECT request_hash,revision FROM studio_collaboration_draft_keys WHERE workspace_id=$1 AND project_id=$2 AND actor_id=$3 AND key_hash=$4`, workspace, project, actor, k[:]).Scan(&digest, &revision)
	} else {
		err = tx.QueryRow(ctx, `SELECT payload_hash,revision FROM studio_draft_idempotency WHERE workspace_id=$1 AND project_id=$2 AND idempotency_key=$3`, workspace, project, key).Scan(&digest, &revision)
	}
	return digest, revision, err
}

func (a *App) registerCollaborationDraftRoutes(mux *http.ServeMux) {
	const draft = "/api/v1/collaboration/projects/{project}/draft"
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "COLLABORATION_STORAGE_REQUIRED", "项目协作需要 PostgreSQL 和账户认证"))
				return
			}
			if p := principalFrom(r); p == nil || p.User == nil {
				writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			if r.Header.Get("X-Project-ID") != "" || r.URL.Query().Has("project_id") {
				writeError(w, failure(404, "PROJECT_NOT_FOUND", "未找到该项目"))
				return
			}
			handler(w, r)
		})
	}
	register("GET "+draft, a.collaborationDraftReadHTTP)
	register("GET "+draft+"/revisions/{revision}", a.collaborationDraftReadHTTP)
	register("GET "+draft+"/receipt", a.collaborationDraftReceiptHTTP)
	register("GET "+draft+"/revisions", a.collaborationDraftHistoryHTTP)
	register("PUT "+draft, a.collaborationDraftSaveHTTP)
}

// The principal remains bound to the member's original login workspace. Only
// this transaction obtains the host workspace of this specific shared project.
func (a *App) collaborationDraftTx(r *http.Request, write bool, fn func(context.Context, pgx.Tx, string, bool) error) error {
	p := principalFrom(r)
	return a.collaborationTx(p, func(ctx context.Context, tx pgx.Tx) error {
		host, role, _, err := collaborationProjectAccessSQL(ctx, tx, p, r.PathValue("project"), write)
		if err != nil {
			return err
		}
		return fn(ctx, tx, host, role != "owner")
	})
}

func (a *App) collaborationDraftReadHTTP(w http.ResponseWriter, r *http.Request) {
	var out ProjectDraft
	var revision uint64
	var err error
	if raw := r.PathValue("revision"); raw != "" {
		revision, err = strconv.ParseUint(raw, 10, 63)
		if err != nil || revision == 0 {
			writeError(w, failure(400, "INVALID_DRAFT_REVISION", "版本号必须为正整数"))
			return
		}
	}
	err = a.collaborationDraftTx(r, false, func(ctx context.Context, tx pgx.Tx, workspace string, _ bool) error {
		var e error
		if revision == 0 {
			out, e = currentDraftSQL(ctx, tx, workspace, r.PathValue("project"))
		} else {
			out, e = readDraftRevisionSQL(ctx, tx, workspace, r.PathValue("project"), revision)
		}
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) collaborationDraftHistoryHTTP(w http.ResponseWriter, r *http.Request) {
	before, limit, err := draftHistoryWindow(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var out draftRevisionPage
	err = a.collaborationDraftTx(r, false, func(ctx context.Context, tx pgx.Tx, workspace string, _ bool) error {
		var e error
		out, e = draftHistorySQL(ctx, tx, workspace, r.PathValue("project"), before, limit)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) collaborationDraftReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	project := r.PathValue("project")
	var out ProjectDraft
	err = a.collaborationDraftTx(r, false, func(ctx context.Context, tx pgx.Tx, workspace string, member bool) error {
		_, rev, e := draftReceiptSQL(ctx, tx, workspace, project, principalFrom(r).User.ID, key, member)
		if errors.Is(e, pgx.ErrNoRows) {
			return failure(404, "DRAFT_RECEIPT_NOT_FOUND", "未找到本人此次保存的收据")
		}
		if e != nil {
			return e
		}
		out, e = readDraftRevisionSQL(ctx, tx, workspace, project, rev)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) collaborationDraftSaveHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	input, err := decodeDraftJSON(w, r)
	if err != nil {
		writeError(w, err)
		return
	}
	var out ProjectDraft
	var replay bool
	err = a.collaborationDraftTx(r, true, func(ctx context.Context, tx pgx.Tx, workspace string, member bool) error {
		var e error
		out, replay, e = saveLockedDraftSQL(ctx, tx, workspace, r.PathValue("project"), principalFrom(r).User.ID, key, input, member)
		return e
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 200, out)
}
