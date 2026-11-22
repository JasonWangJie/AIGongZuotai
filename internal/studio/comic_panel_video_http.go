package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

func panelSource(r *http.Request) (panelVideoSource, error) {
	v, e := comicNumber(r.PathValue("version"))
	if e != nil {
		return panelVideoSource{}, e
	}
	panel := r.PathValue("panel")
	if v > comicVersionLimit || !presentationSlideID.MatchString(panel) {
		return panelVideoSource{}, comicMissing()
	}
	return panelVideoSource{r.PathValue("project"), v, panel}, nil
}

func panelInput(w http.ResponseWriter, r *http.Request, quote bool) (*panelVideoGuard, panelVideoRequest, error) {
	var in panelVideoRequest
	s, e := panelSource(r)
	if e != nil {
		return nil, in, e
	}
	key, e := idempotencyKey(r)
	if e != nil {
		return nil, in, e
	}
	var raw json.RawMessage
	if e = decodeJSON(w, r, &raw); e != nil {
		return nil, in, e
	}
	if quote {
		e = decodeBillingInput(raw, &in.panelVideoOptions, true)
	} else {
		e = decodeBillingInput(raw, &in, false)
	}
	if e != nil {
		return nil, in, failure(400, "INVALID_PANEL_VIDEO_INPUT", "只允许提交本镜头的视频模型及规格、预期尝试和已签发报价")
	}
	if in.ModelID == "" || len(in.ModelID) > 256 || in.ModelVersion == "" || len(in.ModelVersion) > 128 || in.Parameters.Count != 1 || len(in.QuoteID) > 128 || !quote && (in.ExpectedAttempt < 1 || in.ExpectedAttempt > maxPanelVideoAttempts) {
		return nil, in, failure(422, "INVALID_PANEL_VIDEO_INPUT", "请指定视频模型版本、一个视频及有效的预期尝试")
	}
	return newPanelVideoGuard(principalFrom(r), s, key, in), in, nil
}

func (a *App) preparePanelVideo(p *principal, g *panelVideoGuard, in panelVideoOptions) (TaskInput, error) {
	var task TaskInput
	e := a.comicTx(p, g.source.ProjectID, false, func(ctx context.Context, tx pgx.Tx) error {
		d, e := panelVideoDocumentSQL(ctx, tx, g.source, g.workspace)
		if e != nil {
			return e
		}
		g.prompt, e = panelVideoPrompt(d, g.source.PanelID)
		if e != nil {
			return e
		}
		g.attempt, e = g.nextSQL(ctx, tx, false)
		return e
	})
	if e != nil {
		return task, e
	}
	model, e := a.resolveModel(in.ModelID, "video", in.ModelVersion, g.prompt)
	if e != nil {
		return task, e
	}
	params, e := resolveTaskParameters(in.Parameters, model.Model)
	if e != nil {
		return task, e
	}
	if a.media == nil {
		return task, failure(501, "PROVIDER_NOT_CONFIGURED", "当前没有已配置的视频服务")
	}
	task = TaskInput{ProjectID: g.source.ProjectID, Kind: "video", Operation: "generate", ModelID: model.Model.ID, ModelVersion: model.Model.Version, Parameters: params}
	task.Inputs.Prompt = g.prompt
	return task, nil
}

func (a *App) quotePanelVideoHTTP(w http.ResponseWriter, r *http.Request) {
	g, in, e := panelInput(w, r, true)
	if e != nil {
		writeError(w, e)
		return
	}
	p := principalFrom(r)
	if _, found, e := a.panelVideoReceipt(p, g, false); e != nil {
		writeError(w, e)
		return
	} else if found {
		writeError(w, panelVideoConflict())
		return
	}
	task, e := a.preparePanelVideo(p, g, in.panelVideoOptions)
	if e != nil {
		writeError(w, e)
		return
	}
	view := panelVideoPreview{panelVideoOptions: panelVideoOptions{task.ModelID, task.ModelVersion, task.Parameters}, Prompt: g.prompt, Attempt: g.attempt, BillingEnabled: a.pricing != nil}
	if view.BillingEnabled {
		raw, e := json.Marshal(task)
		if e != nil {
			writeError(w, e)
			return
		}
		req := r.WithContext(context.WithValue(r.Context(), panelVideoContextKey{}, g))
		q, e := a.createBillingQuote(req, p, billingQuoteInput{ResourceType: "task", TaskInput: raw})
		if e != nil {
			writeError(w, e)
			return
		}
		view.Quote = &q
	} else {
		e = a.comicTx(p, g.source.ProjectID, false, func(ctx context.Context, tx pgx.Tx) error { return checkWorkspaceGenerationSQL(ctx, tx, g.workspace) })
		if e != nil {
			writeError(w, e)
			return
		}
	}
	writeJSON(w, 200, view)
}

func (a *App) acceptPanelVideoHTTP(w http.ResponseWriter, r *http.Request) {
	g, in, e := panelInput(w, r, false)
	if e != nil {
		writeError(w, e)
		return
	}
	p := principalFrom(r)
	if old, found, e := a.panelVideoReceipt(p, g, true); e != nil {
		writeError(w, e)
		return
	} else if found {
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, 200, old)
		return
	}
	// A concurrent original request can commit between receipt lookup and a
	// definite rejection. Read it before returning a stale capacity/CAS error.
	reject := func(err error) {
		var op *operationError
		if errors.As(err, &op) && op.status < 500 {
			old, found, readErr := a.panelVideoReceipt(p, g, true)
			if readErr != nil {
				writeError(w, readErr)
				return
			}
			if found {
				w.Header().Set("Idempotency-Replayed", "true")
				writeJSON(w, 200, old)
				return
			}
		}
		writeError(w, err)
	}
	task, e := a.preparePanelVideo(p, g, in.panelVideoOptions)
	if e != nil {
		reject(e)
		return
	}
	if in.ExpectedAttempt != g.attempt {
		reject(panelVideoConflict())
		return
	}
	task.QuoteID = in.QuoteID
	ctx := context.WithValue(r.Context(), panelVideoContextKey{}, g)
	_, replayed, e := a.createTaskContext(ctx, task, r.Header.Get("Idempotency-Key"), p.User.ID)
	if e != nil {
		reject(e)
		return
	}
	out, found, e := a.panelVideoReceipt(p, g, true)
	if e != nil {
		writeError(w, e)
		return
	}
	if !found {
		writeError(w, failure(503, "PANEL_VIDEO_RECEIPT_UNAVAILABLE", "请使用原键核对本镜头受理收据"))
		return
	}
	code := 201
	if replayed {
		code = 200
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, code, out)
}

func (a *App) panelVideoReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	s, e := panelSource(r)
	if e != nil {
		writeError(w, e)
		return
	}
	key, e := idempotencyKey(r)
	if e != nil {
		writeError(w, e)
		return
	}
	p := principalFrom(r)
	out, found, e := a.panelVideoReceipt(p, newPanelVideoGuard(p, s, key, panelVideoRequest{}), false)
	if e != nil {
		writeError(w, e)
		return
	}
	if !found {
		writeError(w, failure(404, "PANEL_VIDEO_RECEIPT_NOT_FOUND", "本镜头没有该操作的已受理收据"))
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) listPanelVideosHTTP(w http.ResponseWriter, r *http.Request) {
	s, e := panelSource(r)
	if e != nil {
		writeError(w, e)
		return
	}
	before, limit := maxPanelVideoAttempts+1, 16
	if raw := r.URL.Query().Get("before_attempt"); raw != "" {
		before, e = strconv.Atoi(raw)
		if e != nil || before < 1 || before > maxPanelVideoAttempts+1 {
			writeError(w, failure(400, "INVALID_CURSOR", "镜头尝试分页游标无效"))
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil || limit < 1 || limit > 32 {
			writeError(w, failure(400, "INVALID_LIMIT", "分页数量须为 1 至 32"))
			return
		}
	}
	out, e := a.panelVideoList(principalFrom(r), s, before, limit)
	if e != nil {
		writeError(w, e)
		return
	}
	writeJSON(w, 200, out)
}
