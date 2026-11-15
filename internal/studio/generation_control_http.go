package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"unicode/utf8"
)

// Each endpoint has its own exact key set: Go's case-insensitive field aliases
// must not silently widen this account and operations contract.
func decodeGenerationJSON(w http.ResponseWriter, r *http.Request, out any, keys ...string) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return failure(415, "UNSUPPORTED_MEDIA_TYPE", "请求需要application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	blob, err := io.ReadAll(r.Body)
	if err != nil {
		return failure(413, "GENERATION_BODY_TOO_LARGE", "请求正文最多8KiB")
	}
	if !utf8.Valid(blob) || validateCatalogJSONKeys(blob) != nil {
		return failure(400, "INVALID_JSON", "JSON不能包含重复字段或无效编码")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(blob, &fields) != nil || fields == nil {
		return failure(400, "INVALID_JSON", "需要JSON对象")
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range fields {
		if !allowed[key] {
			return failure(400, "INVALID_JSON", "请求字段名无效")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return failure(400, "INVALID_JSON", "请求正文或字段类型无效")
	}
	return nil
}

func (a *App) generationAccessHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.generationAccess(p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) generationAppealCreateHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in GenerationAppealInput
	if err = decodeGenerationJSON(w, r, &in, "reason_code", "statement"); err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, replayed, err := a.createGenerationAppeal(p, r.PathValue("id"), in, key)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, 201, result)
}

func (a *App) generationAppealHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.readGenerationAppeal(p, p.Workspace.ID, r.PathValue("id"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func generationPageLimit(r *http.Request) (int, error) {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			return 0, failure(400, "INVALID_LIMIT", "每页需要1至50条")
		}
		return limit, nil
	}
	return 20, nil
}

func (a *App) generationAppealsHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := a.requireAccount(r)
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := generationPageLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.listGenerationAppeals(p, p.Workspace.ID, false, r.URL.Query().Get("state"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) operationsGenerationAccessHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "generation_restrict")
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.operationsGenerationAccess(p, workspace)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) operationsGenerationAppealsHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "appeal_resolve")
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := generationPageLimit(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.listGenerationAppeals(p, workspace, true, r.URL.Query().Get("state"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) operationsGenerationAppealHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "appeal_resolve")
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.readGenerationAppeal(p, workspace, r.PathValue("id"), true)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) operationsGenerationPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var in GenerationPlanInput
	if err := decodeGenerationJSON(w, r, &in, "workspace_id", "action", "expected_version", "restriction_id", "appeal_id", "expected_appeal_version", "reason_code", "evidence_ref", "public_reply"); err != nil {
		writeError(w, err)
		return
	}
	permission := "generation_restrict"
	if in.Action == "uphold_restriction" {
		permission = "appeal_resolve"
	}
	p, err := a.requireOperations(r, in.WorkspaceID, permission)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.previewGenerationControl(p, in)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	writeJSON(w, 201, result)
}

func (a *App) operationsGenerationExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		PlanID      string `json:"plan_id"`
		PlanDigest  string `json:"plan_digest"`
		Password    string `json:"password"`
	}
	if err := decodeGenerationJSON(w, r, &in, "workspace_id", "plan_id", "plan_digest", "password"); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "generation_restrict")
	if err != nil {
		writeError(w, err)
		return
	}
	proof, err := a.verifyOperationsPassword(r, in.Password)
	in.Password = ""
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	result, err := a.executeGenerationControl(p, proof, in.WorkspaceID, in.PlanID, in.PlanDigest)
	if err != nil {
		operationsPlanError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) operationsGenerationReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "generation_restrict")
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := a.readGenerationReceipt(p, workspace, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
