package studio

import (
	"context"
	"testing"
)

func TestMediaScheduleOpaqueIdentitySharesLegacyBudget(t *testing.T) {
	a := &taskRecord{Task: Task{ProviderMode: "demo"}, Submission: &MediaSubmission{ProviderIdentity: "original-provider-identity"}, Handle: MediaHandle{ID: "one", ProviderIdentity: "original-provider-identity"}}
	b := cloneLeasedRecord(a)
	b.Handle.ID = "two"
	ka, err := mediaFrozenChannel(a, mediaQueryLane)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := mediaFrozenChannel(b, mediaQueryLane)
	if err != nil || ka != kb || ka != mediaLegacyChannel("demo", mediaQueryLane) {
		t.Fatal("opaque identities did not share conservative budget", err)
	}
	if a.Submission.ProviderIdentity != "original-provider-identity" || b.Handle.ProviderIdentity != "original-provider-identity" {
		t.Fatal("legacy identity bytes replaced")
	}
	s := newMediaScheduler(MediaSchedulingConfig{QueryWorkers: 4, ChannelConcurrency: 1})
	release, ok := s.tryReserve(mediaScheduleCandidate{WorkspaceID: "a", ChannelKey: ka, Lane: mediaQueryLane})
	if !ok {
		t.Fatal("legacy budget absent")
	}
	defer release()
	if _, ok = s.tryReserve(mediaScheduleCandidate{WorkspaceID: "b", ChannelKey: kb, Lane: mediaQueryLane}); ok {
		t.Fatal("opaque alias bypassed shared budget")
	}
	b.Handle.ProviderIdentity = "conflicting-original"
	if _, err = mediaFrozenChannel(b, mediaQueryLane); err == nil {
		t.Fatal("conflicting opaque identities accepted")
	}
}

func TestPostgresMediaScheduleIdentityConflictParksAndRestores(t *testing.T) {
	p := &leaseMedia{}
	a, conn, _ := schedulePostgresFixture(t, p)
	seed := func() string {
		task := leaseTask(t, a)
		_, err := conn.Exec(context.Background(), `UPDATE studio_tasks SET handle='{"ID":"original-handle","Kind":"image","ProviderIdentity":"original-b"}'::jsonb,submission='{"provider_identity":"original-a"}'::jsonb,document=jsonb_set(document,'{status}','"RUNNING"'::jsonb) WHERE id=$1`, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	id := seed()
	a.executePersistentTaskStep(id)
	r, err := a.store.loadTask(id)
	if err != nil || r.Task.Status != "RECONCILE_REQUIRED" || r.Task.Error == nil || r.Task.Error.Code != "MEDIA_SCHEDULE_IDENTITY_CHANGED" || r.Schedule.Active {
		t.Fatal("conflict not publicly parked", err)
	}
	if r.Handle.ProviderIdentity != "original-b" || r.Submission.ProviderIdentity != "original-a" || a.store.failed.Load() {
		t.Fatal("park replaced identity or tripped DB fuse")
	}
	id2 := seed()
	cfg := a.config
	a.Close()
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	r, err = restarted.store.loadTask(id2)
	if err != nil || r.Task.Status != "RECONCILE_REQUIRED" || r.Schedule.Active {
		t.Fatal("startup did not isolate conflicting frozen identity", err)
	}
	if p.submits.Load() != 0 || p.queries.Load() != 0 {
		t.Fatal("conflicting identity reached provider")
	}
}
