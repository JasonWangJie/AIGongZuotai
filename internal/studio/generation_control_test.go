package studio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestGenerationTextAndReasonBoundaries(t *testing.T) {
	for _, s := range []string{"", "复核说明\n第二行", strings.Repeat("字", 341) + "a"} {
		if !validGenerationText(s, false) {
			t.Errorf("valid statement rejected length=%d", len(s))
		}
	}
	for _, s := range []string{strings.Repeat("字", 342), "bad\x00text", "bad\ttext", "bad\rtext", "bad\u007ftext", "bad\u202etext", string([]byte{0xff})} {
		if validGenerationText(s, false) {
			t.Errorf("invalid statement accepted %q", s)
		}
	}
	for _, s := range []string{"", " \n ", strings.Repeat("x", 1025)} {
		if validGenerationText(s, true) {
			t.Error("required public reply accepted empty/oversize")
		}
	}
	if !validGenerationAppealInput(GenerationAppealInput{ReasonCode: "request_review"}) {
		t.Fatal("empty optional statement rejected")
	}
	for _, reason := range []string{"manual_review", "uphold", "", "request_review\x00"} {
		if validGenerationAppealInput(GenerationAppealInput{ReasonCode: reason}) {
			t.Error("invalid user reason", reason)
		}
	}
}
func TestGenerationPlanInputClosesCrossActionFields(t *testing.T) {
	base := GenerationPlanInput{WorkspaceID: newID("ws"), Action: "restrict_future", ExpectedVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:fixture", PublicReply: "暂限制新生成，请申请复核。"}
	if !validGenerationPlanInput(base) {
		t.Fatal("valid restriction rejected")
	}
	for _, alter := range []func(*GenerationPlanInput){func(v *GenerationPlanInput) { v.RestrictionID = newID("grst") }, func(v *GenerationPlanInput) { v.AppealID = newID("gapl") }, func(v *GenerationPlanInput) { v.ExpectedAppealVersion = 1 }, func(v *GenerationPlanInput) { v.ExpectedVersion = 0 }, func(v *GenerationPlanInput) { v.WorkspaceID += "\x00" }, func(v *GenerationPlanInput) { v.EvidenceRef = "https://private.example/evidence" }, func(v *GenerationPlanInput) { v.ReasonCode = "review_complete" }, func(v *GenerationPlanInput) { v.PublicReply = "" }} {
		v := base
		alter(&v)
		if validGenerationPlanInput(v) {
			t.Errorf("invalid restrict accepted %+v", v)
		}
	}
	lift := base
	lift.Action = "lift_restriction"
	lift.RestrictionID = newID("grst")
	lift.ReasonCode = "review_complete"
	if !validGenerationPlanInput(lift) {
		t.Fatal("valid lift rejected")
	}
	lift.AppealID = newID("gapl")
	if validGenerationPlanInput(lift) {
		t.Fatal("lift accepted single appeal target; must close whole open set")
	}
	uphold := base
	uphold.Action = "uphold_restriction"
	uphold.RestrictionID = newID("grst")
	uphold.AppealID = newID("gapl")
	uphold.ExpectedAppealVersion = 1
	if !validGenerationPlanInput(uphold) {
		t.Fatal("valid uphold rejected")
	}
	uphold.ExpectedAppealVersion = 2
	if validGenerationPlanInput(uphold) {
		t.Fatal("uphold accepted resolved appeal")
	}
}
func TestGenerationEffectEpisodeAndReleaseCapacity(t *testing.T) {
	episode, other := newID("grst"), newID("grst")
	open := []generationOpenAppeal{{ID: newID("gapl"), Version: 1}, {ID: newID("gapl"), Version: 1}}
	initial := GenerationAccess{State: "OPEN", Version: 1}
	create := GenerationPlanInput{Action: "restrict_future", ExpectedVersion: 1}
	got, err := generationEffect(initial, create, episode, nil)
	if err != nil || got.ToState != "RESTRICTED" || got.Version != 2 || got.RestrictionID != episode {
		t.Fatalf("initial restriction %+v %v", got, err)
	}
	active := GenerationAccess{State: "RESTRICTED", Version: 2, Restriction: &GenerationRestriction{ID: episode}}
	lift := GenerationPlanInput{Action: "lift_restriction", ExpectedVersion: 2, RestrictionID: episode}
	got, err = generationEffect(active, lift, episode, open)
	if err != nil || got.ToState != "OPEN" || got.Version != 3 || got.AffectedAppeals != 2 {
		t.Fatalf("lift %+v %v", got, err)
	}
	active.Restriction.ID = other
	if _, err = generationEffect(active, lift, episode, open); err == nil {
		t.Fatal("old episode could lift new restriction")
	}
	active.Restriction.ID = episode
	uphold := GenerationPlanInput{Action: "uphold_restriction", ExpectedVersion: 2, RestrictionID: episode, AppealID: open[0].ID, ExpectedAppealVersion: 1}
	got, err = generationEffect(active, uphold, episode, open)
	if err != nil || got.ToState != "RESTRICTED" || got.Version != 2 || got.AffectedAppeals != 1 {
		t.Fatalf("uphold %+v %v", got, err)
	}
	if _, err = generationEffect(active, uphold, episode, nil); err == nil {
		t.Fatal("resolved/missing appeal accepted")
	}
	initial.Version = generationMaxVersion - 1
	create.ExpectedVersion = initial.Version
	if _, err = generationEffect(initial, create, episode, nil); err == nil {
		t.Fatal("restriction consumed required release version")
	}
	active.Version = generationMaxVersion - 1
	lift.ExpectedVersion = active.Version
	if got, err = generationEffect(active, lift, episode, nil); err != nil || got.Version != generationMaxVersion {
		t.Fatal("reserved final lift rejected", got, err)
	}
}
func TestGenerationAppealCursorBindsScope(t *testing.T) {
	scope := generationAppealCursor{Version: 1, User: newID("usr"), Workspace: newID("ws"), Mode: "user", State: "OPEN"}
	valid := scope
	valid.ID = newID("gapl")
	valid.Before = time.Now().UTC()
	encode := func(v generationAppealCursor) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	if got, err := parseGenerationAppealCursor(encode(valid), scope); err != nil || got.ID != valid.ID {
		t.Fatal(got, err)
	}
	for _, alter := range []func(*generationAppealCursor){func(v *generationAppealCursor) { v.User = newID("usr") }, func(v *generationAppealCursor) { v.Workspace = newID("ws") }, func(v *generationAppealCursor) { v.Mode = "operations" }, func(v *generationAppealCursor) { v.State = "all" }, func(v *generationAppealCursor) { v.ID += "\x00" }, func(v *generationAppealCursor) { v.Before = time.Time{} }, func(v *generationAppealCursor) { v.Version = 2 }} {
		v := valid
		alter(&v)
		if _, err := parseGenerationAppealCursor(encode(v), scope); err == nil {
			t.Fatalf("unbound cursor accepted %+v", v)
		}
	}
	for _, raw := range []string{"bad-base64", strings.Repeat("x", 2049)} {
		if _, err := parseGenerationAppealCursor(raw, scope); err == nil {
			t.Fatal("malformed cursor accepted")
		}
	}
}

func TestPostgresGenerationSchemaEvidenceAndScopeAreImmutable(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	user := generationTargetPrincipalFixture(t, f)
	proof := f.proof(t)
	plan := generationPlanFixture(t, f, "restrict_future", 1, "")
	receipt := generationExecuteFixture(t, f, proof, plan)
	appeal, _, err := f.app.createGenerationAppeal(user, plan.RestrictionID, GenerationAppealInput{ReasonCode: "request_review", Statement: "original private statement"}, "schema-appeal-001")
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		query string
		args  []any
	}{
		{`UPDATE studio_generation_restrictions SET public_notice='changed' WHERE id=$1`, []any{plan.RestrictionID}},
		{`DELETE FROM studio_generation_restrictions WHERE id=$1`, []any{plan.RestrictionID}},
		{`UPDATE studio_generation_controls SET state='OPEN',version=3,active_restriction_id=NULL WHERE workspace_id=$1`, []any{plan.WorkspaceID}},
		{`DELETE FROM studio_generation_controls WHERE workspace_id=$1`, []any{plan.WorkspaceID}},
		{`UPDATE studio_generation_appeals SET statement='changed' WHERE id=$1`, []any{appeal.ID}},
		{`DELETE FROM studio_generation_appeal_idempotency WHERE appeal_id=$1`, []any{appeal.ID}},
		{`UPDATE studio_generation_plans SET snapshot='{}' WHERE id=$1`, []any{plan.ID}},
		{`DELETE FROM studio_generation_plans WHERE id=$1`, []any{plan.ID}},
		{`UPDATE studio_generation_receipts SET document='{}' WHERE id=$1`, []any{receipt.ID}},
		{`INSERT INTO studio_generation_appeals(id,workspace_id,restriction_id,applicant_id,reason_code) VALUES($1,$2,$3,$4,'request_review')`, []any{newID("gapl"), f.identity.Workspace.ID, plan.RestrictionID, f.identity.User.ID}},
	}
	for i, m := range mutations {
		if _, e := f.admin.Exec(context.Background(), m.query, m.args...); e == nil {
			t.Fatalf("mutation %d bypassed evidence/scope protection", i)
		}
	}
	lift := generationPlanFixture(t, f, "lift_restriction", 2, plan.RestrictionID)
	generationExecuteFixture(t, f, proof, lift)
	for _, m := range []struct {
		query string
		args  []any
	}{
		{`UPDATE studio_generation_restrictions SET released_at=clock_timestamp() WHERE id=$1`, []any{plan.RestrictionID}},
		{`UPDATE studio_generation_appeals SET public_reply='changed' WHERE id=$1`, []any{appeal.ID}},
	} {
		if _, e := f.admin.Exec(context.Background(), m.query, m.args...); e == nil {
			t.Fatal("terminal evidence mutated")
		}
	}
	if _, e := f.admin.Exec(context.Background(), `INSERT INTO studio_generation_restrictions(id,workspace_id,reason_code,public_notice,evidence_ref,created_by) VALUES($1,$2,'manual_review','orphan','ticket:orphan',$3)`, newID("grst"), f.identity.Workspace.ID, f.identity.User.ID); e == nil {
		t.Fatal("orphan active restriction committed without matching control")
	}
	if f.app.store.failed.Load() {
		t.Fatal("direct constraint negative fixtures contaminated application")
	}
}

func TestPostgresGenerationLiftRetainsCapacityAndBoundsCleanup(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	user := generationTargetPrincipalFixture(t, f)
	proof := f.proof(t)
	plan := generationPlanFixture(t, f, "restrict_future", 1, "")
	generationExecuteFixture(t, f, proof, plan)
	appeal, _, err := f.app.createGenerationAppeal(user, plan.RestrictionID, GenerationAppealInput{ReasonCode: "request_review"}, "capacity-appeal-001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.admin.Exec(context.Background(), `INSERT INTO studio_generation_plans(id,workspace_id,actor_id,action,snapshot,digest,expires_at) SELECT 'gplan_'||md5('capacity-active-'||n::text),$1,$2,'restrict_future','{}',repeat('0',64),clock_timestamp()+interval '10 minutes' FROM generate_series(1,64) n`, plan.WorkspaceID, f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.previewGenerationControl(f.principal, GenerationPlanInput{WorkspaceID: plan.WorkspaceID, Action: "uphold_restriction", ExpectedVersion: 2, RestrictionID: plan.RestrictionID, AppealID: appeal.ID, ExpectedAppealVersion: 1, ReasonCode: "manual_review", EvidenceRef: "ticket:capacity", PublicReply: "暂维持限制"})
	if e, ok := err.(*operationError); !ok || e.code != "GENERATION_PLAN_LIMIT" {
		t.Fatal("active nonlift cap not enforced", err)
	}
	if f.app.store.failed.Load() {
		t.Fatal("business cap tripped database fuse")
	}
	if _, err = f.admin.Exec(context.Background(), `INSERT INTO studio_generation_plans(id,workspace_id,actor_id,action,snapshot,digest,expires_at) SELECT 'gplan_'||md5('capacity-expired-'||n::text),$1,$2,'restrict_future','{}',repeat('0',64),clock_timestamp()-interval '2 days' FROM generate_series(1,1001) n`, plan.WorkspaceID, f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	lift := generationPlanFixture(t, f, "lift_restriction", 2, plan.RestrictionID)
	if got := outboxCount(t, f.admin, `SELECT count(*) FROM studio_generation_plans WHERE expires_at<clock_timestamp()-interval '1 day'`); got != 1 {
		t.Fatalf("cleanup deleted %d instead of bounded 1000", 1001-got)
	}
	newLift := generationPlanFixture(t, f, "lift_restriction", 2, plan.RestrictionID)
	if outboxCount(t, f.admin, `SELECT count(*) FROM studio_generation_plans WHERE id=$1`, lift.ID) != 0 {
		t.Fatal("new lift preview did not supersede reserved pending slot")
	}
	generationExecuteFixture(t, f, proof, newLift)
	if got, err := f.app.readGenerationReceipt(f.principal, plan.WorkspaceID, plan.ID); err != nil || got.PlanID != plan.ID {
		t.Fatal("capacity cleanup deleted committed evidence", got, err)
	}
	if got, err := f.app.generationAccess(user); err != nil || got.State != "OPEN" || got.Version != 3 {
		t.Fatal("full nonlift capacity prevented release", got, err)
	}
}

func generationWaitWorkspaceBlock(t *testing.T, observer pgx.Tx, blockedPID uint32) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := observer.QueryRow(context.Background(), `SELECT pg_backend_pid()=ANY(pg_blocking_pids($1))`, int32(blockedPID)).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("workspace SQL did not wait for the independent transaction lock")
		case <-tick.C:
		}
	}
}
func TestPostgresGenerationFirstRestrictionSerializesWithAcceptance(t *testing.T) {
	f := newOperationsFixture(t)
	f.grant(t, "moderator")
	proof := f.proof(t)
	plan := generationPlanFixture(t, f, "restrict_future", 1, "")
	if outboxCount(t, f.admin, `SELECT count(*) FROM studio_generation_controls WHERE workspace_id=$1`, plan.WorkspaceID) != 0 {
		t.Fatal("preview created control row")
	}
	accept, err := f.admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer accept.Rollback(context.Background())
	if err = checkWorkspaceGenerationSQL(context.Background(), accept, plan.WorkspaceID); err != nil {
		t.Fatal("initial acceptance denied", err)
	}
	pid := f.app.store.conn.PgConn().PID()
	finished := make(chan error, 1)
	executionFinished := make(chan struct{})
	go func() {
		defer close(executionFinished)
		_, e := f.app.executeGenerationControl(f.principal, proof, plan.WorkspaceID, plan.ID, plan.PlanDigest)
		finished <- e
	}()
	defer func() {
		_ = accept.Rollback(context.Background())
		<-executionFinished
	}()
	generationWaitWorkspaceBlock(t, accept, pid)
	select {
	case e := <-finished:
		t.Fatalf("restriction bypassed accepted transaction: %v", e)
	default:
	}
	if err = accept.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restriction did not progress after acceptance committed")
	}
	if outboxCount(t, f.admin, `SELECT count(*) FROM studio_generation_controls WHERE workspace_id=$1 AND state='RESTRICTED' AND version=2`, plan.WorkspaceID) != 1 {
		t.Fatal("first restriction not durable")
	}

	// Reverse ordering on another workspace with no control row: an acceptance
	// begun during the first restriction must read RESTRICTED after that commit.
	writer, err := f.admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	workspace := f.identity.Workspace.ID
	if err = lockGenerationWorkspaceSQL(context.Background(), writer, workspace, true); err != nil {
		t.Fatal(err)
	}
	episode := newID("grst")
	if _, err = writer.Exec(context.Background(), `INSERT INTO studio_generation_restrictions(id,workspace_id,reason_code,public_notice,evidence_ref,created_by) VALUES($1,$2,'manual_review','concurrency fixture','ticket:concurrency',$3)`, episode, workspace, f.identity.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(context.Background(), `INSERT INTO studio_generation_controls(workspace_id,state,version,active_restriction_id) VALUES($1,'RESTRICTED',2,$2)`, workspace, episode); err != nil {
		t.Fatal(err)
	}
	reader, err := pgx.Connect(context.Background(), f.admin.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	readerPID := reader.PgConn().PID()
	readDone := make(chan error, 1)
	readerFinished := make(chan struct{})
	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		defer close(readerFinished)
		tx, e := reader.Begin(readCtx)
		if e != nil {
			readDone <- e
			return
		}
		e = checkWorkspaceGenerationSQL(readCtx, tx, workspace)
		rollbackErr := tx.Rollback(readCtx)
		if e == nil {
			e = rollbackErr
		}
		readDone <- e
	}()
	defer func() {
		cancelRead()
		_ = writer.Rollback(context.Background())
		<-readerFinished
		_ = reader.Close(context.Background())
	}()
	generationWaitWorkspaceBlock(t, writer, readerPID)
	if err = writer.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-readDone:
		if op, ok := err.(*operationError); !ok || op.code != "GENERATION_RESTRICTED" {
			t.Fatal("acceptance used stale missing-row OPEN", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acceptance did not progress after restriction committed")
	}
}
