package studio

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func loadMediaReviewDispatchSQL(ctx context.Context, q sqlQueryRower, taskID string) (*MediaReviewDispatch, error) {
	r := &MediaReviewDispatch{}
	err := q.QueryRow(ctx, `SELECT task_id,workspace_id,project_id,candidate_digest,policy_digest,result_status,state,version,attempt_id,started_at,finished_at FROM studio_media_review_dispatch WHERE task_id=$1`, taskID).Scan(&r.TaskID, &r.WorkspaceID, &r.ProjectID, &r.CandidateDigest, &r.PolicyDigest, &r.ResultStatus, &r.State, &r.Version, &r.AttemptID, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func sameMediaReviewIdentity(a, b *MediaReviewDispatch) bool {
	return a.TaskID == b.TaskID && a.WorkspaceID == b.WorkspaceID && a.ProjectID == b.ProjectID && a.CandidateDigest == b.CandidateDigest && a.PolicyDigest == b.PolicyDigest && a.ResultStatus == b.ResultStatus
}

func sameMediaReviewTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
func sameMediaReviewRecord(a, b *MediaReviewDispatch) bool {
	return sameMediaReviewIdentity(a, b) && a.State == b.State && a.Version == b.Version && a.AttemptID == b.AttemptID && sameMediaReviewTime(a.StartedAt, b.StartedAt) && sameMediaReviewTime(a.FinishedAt, b.FinishedAt)
}

// Called after the task lease fence and delivery save. The pre-candidate check
// occurs before B2 insertion, so a legacy candidate can never acquire READY.
func saveTaskReviewAndModerationSQL(ctx context.Context, tx pgx.Tx, t *taskRecord) error {
	old, err := loadMediaReviewDispatchSQL(ctx, tx, t.Task.ID)
	if err != nil {
		return err
	}
	r := cloneMediaReviewDispatch(t.ReviewDispatch)
	if r == nil {
		r = cloneMediaReviewDispatch(old)
	}
	if old == nil && r != nil {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM studio_task_media_candidates WHERE task_id=$1)", t.Task.ID).Scan(&exists); err != nil {
			return err
		}
		if exists || t.Lease == nil || r.State != "READY" || r.Version != 0 || r.AttemptID != "" || r.StartedAt != nil || r.FinishedAt != nil {
			return mediaReviewStateError()
		}
	}
	if err = saveTaskMediaModerationSQL(ctx, tx, t.Task, t.MediaModeration); err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	state, err := loadTaskMediaModerationSQL(ctx, tx, t.Task.ID)
	if err != nil {
		return err
	}
	if state == nil || len(state.Candidates) == 0 || t.Task.Moderation == nil || r.TaskID != t.Task.ID || r.WorkspaceID != t.Task.WorkspaceID || r.ProjectID != t.Task.ProjectID || r.PolicyDigest != state.Policy.Digest || !moderationDigestPattern.MatchString(r.CandidateDigest) || r.ResultStatus != "SUCCEEDED" && r.ResultStatus != "PARTIAL" {
		return mediaReviewStateError()
	}
	digest, err := mediaReviewCandidateDigest(state.Candidates, t.Task.Kind)
	if err != nil {
		return err
	}
	if digest != r.CandidateDigest {
		return mediaReviewStateError()
	}
	if t.Task.Parameters == nil || len(state.Candidates) > t.Task.Parameters.Count || r.ResultStatus == "SUCCEEDED" && len(state.Candidates) != t.Task.Parameters.Count || r.ResultStatus == "PARTIAL" && len(state.Candidates) >= t.Task.Parameters.Count {
		return mediaReviewStateError()
	}
	if old != nil && (!sameMediaReviewIdentity(old, r) || old.Version != r.Version) {
		return mediaReviewStateError()
	}
	finished := t.Task.Moderation.State != "PENDING"
	if finished {
		if !terminal(t.Task.Status) && t.Task.Status != "RECONCILE_REQUIRED" && t.Task.Status != "SUBMIT_UNKNOWN" {
			return mediaReviewStateError()
		}
		if t.Task.Moderation.State != "REVIEW" && (old == nil || old.State != "STARTED" && old.State != "FINISHED") {
			return mediaReviewStateError()
		}
		if t.Task.Moderation.State == "ALLOWED" && t.Task.Status != r.ResultStatus {
			return mediaReviewStateError()
		}
		r.State = "FINISHED"
		if r.FinishedAt == nil {
			now := time.Now().UTC().Truncate(time.Microsecond)
			r.FinishedAt = &now
		}
	} else if t.Task.Status != "MODERATING" || r.State == "FINISHED" || len(t.Task.Artifacts) != 0 {
		return mediaReviewStateError()
	}
	if old == nil {
		if r.State != "READY" {
			return mediaReviewStateError()
		}
		r.Version = 1
		_, err = tx.Exec(ctx, `INSERT INTO studio_media_review_dispatch(task_id,workspace_id,project_id,candidate_digest,policy_digest,result_status,state,version) VALUES($1,$2,$3,$4,$5,$6,'READY',1)`, r.TaskID, r.WorkspaceID, r.ProjectID, r.CandidateDigest, r.PolicyDigest, r.ResultStatus)
	} else {
		if r.AttemptID != old.AttemptID || !sameMediaReviewTime(r.StartedAt, old.StartedAt) {
			if old.State != "READY" || r.State != "STARTED" || !mediaReviewAttemptID.MatchString(r.AttemptID) || r.StartedAt == nil || t.Lease == nil || t.LeaseComplete {
				return mediaReviewStateError()
			}
		}
		if r.State == old.State {
			if !sameMediaReviewRecord(r, old) {
				return mediaReviewStateError()
			}
		} else {
			if old.State == "FINISHED" || old.State == "READY" && r.State != "STARTED" && r.State != "FINISHED" || old.State == "STARTED" && r.State != "FINISHED" {
				return mediaReviewStateError()
			}
			if r.State == "STARTED" && (r.AttemptID == "" || r.StartedAt == nil) {
				return mediaReviewStateError()
			}
			r.Version = old.Version + 1
			tag, e := tx.Exec(ctx, `UPDATE studio_media_review_dispatch SET state=$2,version=$3,attempt_id=$4,started_at=$5,finished_at=$6 WHERE task_id=$1 AND version=$7 AND state=$8`, r.TaskID, r.State, r.Version, r.AttemptID, r.StartedAt, r.FinishedAt, old.Version, old.State)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return mediaReviewStateError()
			}
		}
	}
	if err != nil {
		return err
	}
	t.ReviewDispatch = r
	return nil
}
