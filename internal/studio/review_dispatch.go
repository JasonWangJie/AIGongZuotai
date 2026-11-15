package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

// Private evidence of whether the only permitted batch review has begun.
// It never appears in Task, browser events, audit bodies, or metric labels.
type MediaReviewDispatch struct {
	TaskID, WorkspaceID, ProjectID string
	CandidateDigest, PolicyDigest  string
	ResultStatus, State            string
	Version                        uint64
	AttemptID                      string
	StartedAt, FinishedAt          *time.Time
}

var mediaReviewAttemptID = regexp.MustCompile(`^review_[a-f0-9]{32}$`)

func cloneMediaReviewDispatch(r *MediaReviewDispatch) *MediaReviewDispatch {
	if r == nil {
		return nil
	}
	copy := *r
	if r.StartedAt != nil {
		v := *r.StartedAt
		copy.StartedAt = &v
	}
	if r.FinishedAt != nil {
		v := *r.FinishedAt
		copy.FinishedAt = &v
	}
	return &copy
}
func mediaReviewCandidateDigest(candidates []Artifact, kind string) (string, error) {
	encoded, err := encodeMediaCandidates(candidates, kind)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
func mediaReviewStateError() error {
	return failure(409, "MEDIA_REVIEW_STATE_CHANGED", "首次媒体审核证据已变化，原候选保持隔离")
}

func newMediaReviewDispatch(t *taskRecord, status string) (*MediaReviewDispatch, error) {
	if t.MediaModeration == nil || t.MediaModeration.Policy == nil || len(t.MediaModeration.Candidates) == 0 || t.Task.Moderation == nil || t.Task.Moderation.State != "PENDING" || status != "SUCCEEDED" && status != "PARTIAL" {
		return nil, mediaReviewStateError()
	}
	digest, err := mediaReviewCandidateDigest(t.MediaModeration.Candidates, t.Task.Kind)
	if err != nil {
		return nil, err
	}
	return &MediaReviewDispatch{TaskID: t.Task.ID, WorkspaceID: t.Task.WorkspaceID, ProjectID: t.Task.ProjectID, CandidateDigest: digest, PolicyDigest: t.MediaModeration.Policy.Digest, ResultStatus: status, State: "READY"}, nil
}
