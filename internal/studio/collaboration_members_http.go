package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

func decodeCollaborationMutation(w http.ResponseWriter, r *http.Request, op string) (in collaborationMutation, err error) {
	media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || media != "application/json" {
		return in, failure(415, "JSON_REQUIRED", "请使用 application/json")
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var size *http.MaxBytesError
	if errors.As(e, &size) {
		return in, failure(413, "COLLABORATION_BODY_LIMIT", "邀请或成员操作正文最多 4 KiB")
	}
	if e != nil || !utf8.Valid(raw) {
		return in, failure(400, "INVALID_JSON", "请提交一份有效的 UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var expected *uint64
	switch op {
	case "invite":
		var b struct {
			Target   string  `json:"target_user_id"`
			Role     string  `json:"role"`
			Expected *uint64 `json:"expected_version"`
		}
		e = d.Decode(&b)
		in.Target, in.Role, expected = b.Target, b.Role, b.Expected
	case "accept":
		var b struct {
			Expected *uint64 `json:"expected_version"`
		}
		e = d.Decode(&b)
		in.Invitation, expected = r.PathValue("invitation"), b.Expected
	case "withdraw", "downgrade", "revoke":
		var b struct {
			Invitation string  `json:"invitation_id"`
			Expected   *uint64 `json:"expected_version"`
		}
		e = d.Decode(&b)
		in.Target, in.Invitation, expected = r.PathValue("user"), b.Invitation, b.Expected
	default:
		return in, collaborationMissing()
	}
	if e != nil || d.Decode(new(any)) != io.EOF {
		return in, failure(400, "INVALID_JSON", "请只提供此次操作需要的 JSON 字段")
	}
	if expected == nil {
		return in, failure(422, "EXPECTED_VERSION_REQUIRED", "请提供所见成员版本，首次邀请填写 0")
	}
	in.Project, in.ExpectedVersion = r.PathValue("project"), *expected
	return in, nil
}

func (a *App) registerCollaborationMembershipRoutes(mux *http.ServeMux) {
	const personal = "/api/v1/collaboration"
	const project = "/api/v1/projects/{project}/collaboration"
	register := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			if a.store == nil || a.config.AuthMode != "required" {
				writeError(w, failure(501, "COLLABORATION_STORAGE_REQUIRED", "项目协作需要持久账户模式"))
				return
			}
			if principalFrom(r) == nil || principalFrom(r).User == nil {
				writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
				return
			}
			if id := r.PathValue("project"); id != "" && ((r.Header.Get("X-Project-ID") != "" && r.Header.Get("X-Project-ID") != id) || (r.URL.Query().Get("project_id") != "" && r.URL.Query().Get("project_id") != id)) {
				writeError(w, collaborationMissing())
				return
			}
			handler(w, r)
		})
	}
	for _, kind := range []string{"invitations", "shared-projects", "members"} {
		path := personal + "/" + kind
		if kind == "members" {
			path = project + "/members"
		}
		register("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			page, e := a.collaborationList(principalFrom(r), r.PathValue("project"), kind, r.URL.Query().Get("target_user_id"))
			if e != nil {
				writeError(w, e)
				return
			}
			writeJSON(w, 200, page)
		})
	}
	register("GET "+personal+"/receipt", func(w http.ResponseWriter, r *http.Request) {
		key, e := idempotencyKey(r)
		if e != nil {
			writeError(w, e)
			return
		}
		receipt, e := a.collaborationReceipt(principalFrom(r), r.URL.Query().Get("operation"), key)
		if e != nil {
			writeError(w, e)
			return
		}
		writeJSON(w, 200, receipt)
	})
	mutate := func(op string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			key, e := idempotencyKey(r)
			if e != nil {
				writeError(w, e)
				return
			}
			in, e := decodeCollaborationMutation(w, r, op)
			if e != nil {
				writeError(w, e)
				return
			}
			receipt, replayed, e := a.mutateCollaboration(principalFrom(r), op, key, in)
			if e != nil {
				writeError(w, e)
				return
			}
			if replayed {
				w.Header().Set("Idempotency-Replayed", "true")
			}
			status := http.StatusOK
			if op == "invite" {
				status = http.StatusCreated
			}
			writeJSON(w, status, receipt)
		}
	}
	register("POST "+project+"/invitations", mutate("invite"))
	register("POST "+personal+"/invitations/{invitation}/accept", mutate("accept"))
	for _, op := range []string{"withdraw", "downgrade", "revoke"} {
		register("POST "+project+"/members/{user}/"+op, mutate(op))
	}
}
