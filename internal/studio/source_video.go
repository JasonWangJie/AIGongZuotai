package studio

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
)

type SourceVideoRef struct {
	AssetID       string `json:"asset_id"`
	AssetRevision uint64 `json:"asset_revision"`
}

type SourceVideoCapabilities struct {
	EditPresets   []string `json:"data_uri_edit_presets,omitempty"`
	ExtendPresets []string `json:"data_uri_extend_presets,omitempty"`
	Directions    []string `json:"extension_directions,omitempty"`
}

func (c *SourceVideoCapabilities) clone() *SourceVideoCapabilities {
	if c == nil {
		return nil
	}
	return &SourceVideoCapabilities{EditPresets: slices.Clone(c.EditPresets), ExtendPresets: slices.Clone(c.ExtendPresets), Directions: slices.Clone(c.Directions)}
}

func (c *SourceVideoCapabilities) validate(kind string, presets []ParameterPreset) error {
	if c == nil {
		return nil
	}
	if kind != "video" || len(c.EditPresets)+len(c.ExtendPresets) == 0 || len(c.ExtendPresets) == 0 && len(c.Directions) > 0 || len(c.Directions) > 16 {
		return errors.New("source_video requires an explicit video operation and its presets")
	}
	available := map[string]bool{}
	for _, p := range presets {
		available[p.ID] = true
	}
	for _, list := range [][]string{c.EditPresets, c.ExtendPresets, c.Directions} {
		seen := map[string]bool{}
		for _, v := range list {
			if seen[v] || !catalogID.MatchString(v) {
				return errors.New("invalid or repeated source_video option")
			}
			seen[v] = true
		}
	}
	for _, list := range [][]string{c.EditPresets, c.ExtendPresets} {
		for _, id := range list {
			if !available[id] {
				return errors.New("source_video references an unknown preset")
			}
		}
	}
	for _, direction := range c.Directions {
		if !videoDirectionName.MatchString(direction) {
			return errors.New("invalid source_video extension direction")
		}
	}
	return nil
}

func sourceVideoOperation(op string) bool { return op == "video_edit" || op == "video_extend" }

func (a *App) sourceVideoReceipt(p *principal, project, key string) (Task, error) {
	var task Task
	err := a.videoTimelineTx(p, project, false, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT t.document FROM studio_idempotency k
 JOIN studio_task_video_sources s ON s.task_id=k.resource_id
 JOIN studio_tasks t ON (t.id,t.project_id,t.workspace_id)=(s.task_id,s.project_id,s.workspace_id)
 WHERE k.scope_key=$1 AND k.resource_type='task' AND s.project_id=$2 AND s.workspace_id=$3 AND s.actor_id=$4`,
			p.Workspace.ID+":tasks:"+key, project, p.Workspace.ID, p.User.ID).Scan(&task)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = failure(404, "TASK_RECEIPT_NOT_FOUND", "此账户与项目下尚未查到原视频任务收据")
	}
	return copyTask(task), err
}

func (a *App) sourceVideoReceiptHTTP(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	p := principalFrom(r)
	if p == nil || p.User == nil {
		writeError(w, failure(401, "AUTH_REQUIRED", "请先登录"))
		return
	}
	if r.Header.Get("X-Project-ID") != p.Project.ID {
		writeError(w, failure(400, "PROJECT_REQUIRED", "请提供原任务的项目请求头"))
		return
	}
	task, err := a.sourceVideoReceipt(p, p.Project.ID, key)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, task)
}

func sourceVideoInputOperation(input TaskInput) (string, error) {
	ref := input.Inputs.SourceVideo
	otherInputs := input.WorkflowAttemptID != "" || input.Inputs.WorkflowID != "" ||
		input.Inputs.FirstFrame != nil || len(input.Inputs.ReferenceAssetIDs) > 0
	validRef := ref != nil && libraryIDPattern.MatchString(ref.AssetID) && ref.AssetRevision >= 1 && ref.AssetRevision <= libraryRevisionLimit
	direction := input.Inputs.ExtensionDirection
	if !sourceVideoOperation(input.Operation) || input.Kind != "video" || input.ModelVersion == "" || input.Parameters.Count != 1 ||
		otherInputs || !validRef || direction != "" && (input.Operation != "video_extend" || !videoDirectionName.MatchString(direction)) {
		return "", failure(422, "INVALID_VIDEO_SOURCE", "请选择项目内的视频修订及对应操作、模型和规格")
	}
	return input.Operation, nil
}

func validateSourceVideoPreset(input TaskInput, m Model) error {
	c := m.Capabilities.SourceVideo
	if c != nil {
		allowed := c.EditPresets
		if input.Operation == "video_extend" {
			allowed = c.ExtendPresets
		}
		for _, p := range m.Capabilities.ParameterPresets {
			if p.matches(input.Parameters) && slices.Contains(allowed, p.ID) &&
				(input.Inputs.ExtensionDirection == "" || slices.Contains(c.Directions, input.Inputs.ExtensionDirection)) {
				return nil
			}
		}
	}
	return failure(422, "VIDEO_SOURCE_SPEC_UNAVAILABLE", "此模型尚未配置该原视频操作、完整规格或延长方向")
}

func sourceVideoInfoSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, ref SourceVideoRef) (timelineVideoAsset, error) {
	sources, err := videoTimelineAssetsSQL(ctx, tx, scope, []VideoTimelineClip{{AssetID: ref.AssetID}})
	if err != nil {
		return timelineVideoAsset{}, err
	}
	source, ok := sources[ref.AssetID]
	if !ok || source.revision != ref.AssetRevision {
		return timelineVideoAsset{}, videoTimelineAssetUnavailable()
	}
	// A registered delivery receipt and a frozen moderation candidate, when present,
	// must identify this very artifact. The task and asset rows are already locked.
	content := source.info
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM studio_tasks t
 LEFT JOIN studio_task_media_moderation m ON m.task_id=t.id
 LEFT JOIN studio_media_deliveries d ON d.task_id=t.id
 WHERE t.id=$1 AND t.workspace_id=$2 AND t.project_id=$3
 AND (d.task_id IS NULL OR (d.state='COMPLETE' AND EXISTS(SELECT 1 FROM studio_media_delivery_items i WHERE i.task_id=t.id AND i.receipt = $4::jsonb)))
 AND ((m.task_id IS NULL AND (t.document->'moderation' IS NULL OR t.document->'moderation'='null'::jsonb))
 OR (m.state='ALLOWED' AND t.document->'moderation'->>'state'='ALLOWED' AND EXISTS(
 SELECT 1 FROM studio_task_media_candidates c, jsonb_array_elements(c.candidates) AS candidate
 WHERE c.task_id=t.id AND candidate->'storage_info' = $4::jsonb))))`, source.taskID, scope.WorkspaceID, scope.ProjectID, content).Scan(&eligible)
	if err != nil {
		return timelineVideoAsset{}, err
	}
	if !eligible {
		return timelineVideoAsset{}, videoTimelineAssetUnavailable()
	}
	return source, nil
}

func (a *App) resolveSourceVideo(p *principal, input TaskInput, model Model) (*timelineVideoAsset, error) {
	if a.store == nil || a.config.AuthMode != "required" || p == nil || p.User == nil {
		return nil, failure(422, "VIDEO_SOURCE_UNAVAILABLE", "原视频操作需要已登录的持久项目")
	}
	if _, ok := a.media.(sourceVideoProvider); !ok {
		return nil, failure(422, "VIDEO_SOURCE_UNAVAILABLE", "当前渠道未配置原视频操作")
	}
	if err := validateSourceVideoPreset(input, model); err != nil {
		return nil, err
	}
	var source timelineVideoAsset
	err := a.videoTimelineTx(p, input.ProjectID, false, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		source, e = sourceVideoInfoSQL(ctx, tx, subscriptionScope{p.Workspace.ID, input.ProjectID}, *input.Inputs.SourceVideo)
		return e
	})
	return &source, err
}

func (a *App) prepareSourceVideo(ctx context.Context, input TaskInput, upstream string, source AssetInfo) (*MediaSubmission, error) {
	select {
	case a.editPrepareSlots <- struct{}{}:
		defer func() { <-a.editPrepareSlots }()
	default:
		return nil, failure(429, "REFERENCE_PREPARATION_BUSY", "私有输入准备繁忙")
	}
	p := a.media.(sourceVideoProvider)
	sub, err := p.PrepareSourceVideoModel(ctx, input, upstream, input.Inputs.ExtensionDirection, a.privateMediaSource(ctx, source, "video"))
	if err != nil {
		var pe *MediaProviderError
		if errors.As(err, &pe) {
			switch pe.Code {
			case "REFERENCE_PREPARATION_BUSY", "ASSET_STORAGE_BUSY":
				return nil, failure(429, pe.Code, "私有输入准备繁忙")
			case "REFERENCE_STORAGE_FULL":
				return nil, failure(507, pe.Code, "私有输入存储容量已满")
			}
		}
		return nil, failure(422, "VIDEO_SOURCE_UNAVAILABLE", "原视频读取或冻结失败，请核对项目素材")
	}
	return &sub, nil
}
