package studio

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

func workflowFirstFrameUnavailable() error {
	return failure(409, "WORKFLOW_FIRST_FRAME_UNAVAILABLE", "原选图已不可用，无法受理首帧视频")
}

func workflowFirstFrameSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, x WorkflowExecution, selected WorkflowCandidate, lock bool) (AssetInfo, error) {
	if !x.Graph.hasVideo() || x.Status != "VIDEO_READY" || x.Selected == nil || *x.Selected != selected || x.ImageTaskID != selected.TaskID {
		return AssetInfo{}, workflowFirstFrameUnavailable()
	}
	return workflowSelectedAssetSQL(ctx, tx, scope, selected, lock)
}

func workflowSelectedAssetSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, selected WorkflowCandidate, lock bool) (AssetInfo, error) {
	records, err := workflowAssetsSQL(ctx, tx, scope, selected.TaskID, "image", lock)
	if err != nil {
		return AssetInfo{}, err
	}
	for _, r := range records {
		c := r.candidate
		if c.AssetID == selected.AssetID && c.ArtifactID == selected.ArtifactID && c.TaskID == selected.TaskID && c.ContentVersion == selected.ContentVersion &&
			r.info != nil && validAssetInfo(*r.info, c.ArtifactID) && strings.HasPrefix(r.info.ContentType, "image/") {
			return *r.info, nil
		}
	}
	return AssetInfo{}, workflowFirstFrameUnavailable()
}

// This lookup only supplies private metadata. The media adapter opens the
// original, verifies its bytes and freezes the multipart body outside SQL.
func (s *postgresStore) workflowFirstFrameInfo(scope subscriptionScope, id string, selected WorkflowCandidate, inputs ...TaskInput) (info AssetInfo, err error) {
	err = s.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		x, e := workflowExecutionSQL(ctx, tx, scope, id)
		if e != nil {
			return e
		}
		if len(inputs) == 0 {
			info, e = workflowFirstFrameSQL(ctx, tx, scope, x, selected, false)
		} else {
			if len(inputs) != 1 || inputs[0].Inputs.FirstFrame == nil || *inputs[0].Inputs.FirstFrame != selected {
				return workflowFirstFrameUnavailable()
			}
			for _, n := range x.Graph.Nodes {
				if n.Kind == "video" {
					info, e = workflowMediaInputSQL(ctx, tx, scope, x, n, inputs[0], false)
					break
				}
			}
			if e == nil && info.ID == "" {
				return workflowFirstFrameUnavailable()
			}
		}
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return
}

func workflowVideoArtifactsSQL(ctx context.Context, tx pgx.Tx, scope subscriptionScope, id string, lock bool) ([]WorkflowCandidate, error) {
	records, err := workflowAssetsSQL(ctx, tx, scope, id, "video", lock)
	if err != nil {
		return nil, err
	}
	result := []WorkflowCandidate{}
	for _, r := range records {
		if r.info != nil && r.info.ContentType == "video/mp4" && validAssetInfo(*r.info, r.candidate.ArtifactID) {
			result = append(result, r.candidate)
		}
	}
	return result, nil
}
