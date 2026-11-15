package studio

import (
	"testing"
	"time"
)

func TestMediaReviewDispatchTimestampIdentityAndTimeout(t *testing.T) {
	start := time.Date(2026, 11, 15, 0, 0, 0, 123456000, time.UTC)
	local := start.In(time.FixedZone("fixture-offset", 8*3600))
	if !sameMediaReviewTime(&start, &local) || sameMediaReviewTime(nil, &start) || !sameMediaReviewTime(nil, nil) {
		t.Fatal("same instant/null identity changed")
	}
	changed := start.Add(time.Microsecond)
	if sameMediaReviewTime(&start, &changed) {
		t.Fatal("changed microsecond accepted")
	}
	r := &MediaReviewDispatch{State: "STARTED", Version: 2, AttemptID: "original-attempt", StartedAt: &start}
	copy := cloneMediaReviewDispatch(r)
	copy.StartedAt = &local
	if !sameMediaReviewRecord(r, copy) {
		t.Fatal("database timezone changed semantic record")
	}
	copy.AttemptID = "different-attempt"
	if sameMediaReviewRecord(r, copy) {
		t.Fatal("attempt identity ignored")
	}
	for count := 1; count <= 4; count++ {
		task := &taskRecord{MediaModeration: &MediaModerationRecord{Candidates: make([]Artifact, count)}}
		want := time.Duration(count)*3*time.Minute + 30*time.Second
		if got := mediaReviewTimeout(task); got != want || got > 12*time.Minute+30*time.Second {
			t.Fatal("batch deadline is not bounded by per-item work", count, got)
		}
	}
}
