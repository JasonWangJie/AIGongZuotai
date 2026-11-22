package studio

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPostgresCollaborationUpgrade031PreservesAuthorAndLegacyDraftKey(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	c := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "031")
	tx, e := c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing development workspace');
	INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing development project');
	INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_before','before@membership.test','owner',decode('00','hex'));
	INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_before','usr_before','旧工作区');
	INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_before','ws_before','旧项目');
	INSERT INTO studio_project_drafts(project_id,workspace_id,revision,updated_at) VALUES('prj_before','ws_before',1,clock_timestamp());
	INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at)
	VALUES('prj_before','ws_before',1,'[{"id":"old","title":"保留原稿","note":"原文"}]',1,clock_timestamp());
	INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at)
	VALUES('prj_before','ws_before','old-author-stable-key',decode(repeat('ab',32),'hex'),1,clock_timestamp());
	INSERT INTO studio_comics(project_id,workspace_id,version) VALUES('prj_before','ws_before',1);
	INSERT INTO studio_comic_versions(project_id,workspace_id,version,created_by,document)
	VALUES('prj_before','ws_before',1,'usr_before','{"title":"清晨","story":"车驶进车站。","characters":[{"id":"r","name":"齐","description":"主角"}],"scenes":[{"id":"station","name":"车站","description":"清晨"}],"panels":[{"id":"one","title":"开场","scene_id":"station","character_ids":["r"],"action":"进站","dialogue":"到了。"}]}');
	INSERT INTO studio_comic_receipts(project_id,workspace_id,actor_id,operation,key_hash,request_hash,version) VALUES
	 ('prj_before','ws_before','usr_before','save',decode(repeat('ed',32),'hex'),decode(repeat('cd',32),'hex'),1),
	 ('prj_before','ws_before','usr_before','confirm',decode(repeat('cc',32),'hex'),decode(repeat('ee',32),'hex'),1)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_workspaces", "studio_projects", "studio_users", "studio_sessions", "studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency", "studio_comics", "studio_comic_versions", "studio_comic_assets", "studio_comic_receipts", "studio_operations_grants", "studio_ledger_wallets"}
	before := schedulerOriginalSnapshot(t, c, tables)
	store, e := openPostgres(dsn)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, c, tables)) {
		t.Fatal("032 changed old draft, author, confirmation or home access")
	}
	if sum := outboxCount(t, c, "SELECT (SELECT count(*) FROM studio_collaboration_members)+(SELECT count(*) FROM studio_collaboration_receipts)+(SELECT count(*) FROM studio_collaboration_draft_keys)"); sum != 0 {
		t.Fatal("migration invented member access or keys", sum)
	}
	tx, e = c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("031 writer accepted 032", e)
	}
}

func TestPostgresCollaborationConcurrentAcceptOrWithdrawHasOneReceipt(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	oc, mc := authClient(t), authClient(t)
	owner := registerFixture(t, oc, s.URL, "race-owner@example.test")
	member := registerFixture(t, mc, s.URL, "race-member@example.test")
	op, e := a.currentPrincipal(operationsRequest(t, oc, s))
	if e != nil {
		t.Fatal(e)
	}
	mp, e := a.currentPrincipal(operationsRequest(t, mc, s))
	if e != nil {
		t.Fatal(e)
	}
	initial, _, e := a.mutateCollaboration(op, "invite", newID("invite"), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Role: "viewer"})
	if e != nil {
		t.Fatal(e)
	}
	admin := deliveryAdmin(t, dsn)
	barrier, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer barrier.Rollback(ctx)
	if _, e = barrier.Exec(ctx, "SELECT id FROM studio_projects WHERE id=$1 FOR UPDATE", owner.Project.ID); e != nil {
		t.Fatal(e)
	}
	type action struct {
		p       *principal
		op, key string
		in      collaborationMutation
	}
	type outcome struct {
		action
		entry CollaborationReceipt
		err   error
	}
	results := make(chan outcome, 2)
	var pids []int32
	for _, request := range []action{
		{mp, "accept", newID("accept"), collaborationMutation{Invitation: initial.InvitationID, ExpectedVersion: 1}},
		{op, "withdraw", newID("withdraw"), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Invitation: initial.InvitationID, ExpectedVersion: 1}},
	} {
		conn := deliveryAdmin(t, dsn)
		pids = append(pids, int32(conn.PgConn().PID()))
		writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}}
		go func() {
			result, _, e := writer.mutateCollaboration(request.p, request.op, request.key, request.in)
			results <- outcome{request, result, e}
		}()
	}
	awaitRunLeaseWaiters(t, deliveryAdmin(t, dsn), pids...)
	if e = barrier.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	success := 0
	state := ""
	for i := 0; i < 2; i++ {
		got := <-results
		read, e := a.collaborationReceipt(got.p, got.op, got.key)
		if got.err == nil {
			success++
			state = got.entry.State
			if e != nil || read.Version != 2 || read.State != state {
				t.Fatal("winning original receipt", e)
			}
		} else {
			requireOperation(t, got.err, "COLLABORATION_VERSION_CONFLICT")
			requireOperation(t, e, "COLLABORATION_NOT_FOUND")
		}
	}
	role, _, e := collaborationTestAccess(a, mp, owner.Project.ID, false)
	if success != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_collaboration_receipts") != 2 ||
		state == "ACTIVE" && (e != nil || role != "viewer") || state == "WITHDRAWN" && e == nil {
		t.Fatal("atomic accept versus withdrawal failed", state, e)
	}
}

func TestPostgresCollaborationLostAcceptAcknowledgement(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	direct := f.cfg.DatabaseURL
	admin := deliveryAdmin(t, direct)
	proxy, armed, dropped := recoveryControlAckProxy(t, direct)
	f.cfg.DatabaseURL = proxy
	a, s := f.open(t)
	oc, mc := authClient(t), authClient(t)
	owner := registerFixture(t, oc, s.URL, "ack-owner@example.test")
	member := registerFixture(t, mc, s.URL, "ack-recipient@example.test")
	op, e := a.currentPrincipal(operationsRequest(t, oc, s))
	if e != nil {
		t.Fatal(e)
	}
	inv, _, e := a.mutateCollaboration(op, "invite", newID("invite"), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Role: "editor"})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = admin.Exec(ctx, `CREATE FUNCTION hold_collaboration_accept() RETURNS trigger LANGUAGE plpgsql AS $$
	 BEGIN PERFORM pg_advisory_xact_lock(912330,32); RETURN NEW; END $$;
	 CREATE TRIGGER hold_collaboration_accept BEFORE INSERT ON studio_collaboration_receipts FOR EACH ROW WHEN (NEW.operation='accept') EXECUTE FUNCTION hold_collaboration_accept()`)
	if e != nil {
		t.Fatal(e)
	}
	barrier, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer barrier.Rollback(ctx)
	if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912330,32)"); e != nil {
		t.Fatal(e)
	}
	key := newID("accept")
	result := make(chan int, 1)
	go func() {
		code, _, _ := authRequest(t, mc, s.URL, "POST", "/api/v1/collaboration/invitations/"+inv.InvitationID+"/accept", key, "", map[string]uint64{"expected_version": 1})
		result <- code
	}()
	verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, result)
	s.Close()
	a.Close()
	f.cfg.DatabaseURL = direct
	_, restored := f.open(t)
	data, _ := presentationRequest(t, mc, restored.URL, "", "GET", "/api/v1/collaboration/receipt?operation=accept", key, nil, 200)
	read := decode[CollaborationReceipt](t, data)
	if read.InvitationID != inv.InvitationID || read.Version != 2 || read.TargetUserID != member.User.ID || read.State != "ACTIVE" || outboxCount(t, admin, "SELECT count(*) FROM studio_collaboration_receipts WHERE operation='accept'") != 1 {
		t.Fatal("lost reply duplicated or lost original acceptance")
	}
	presentationRequest(t, oc, restored.URL, "", "GET", "/api/v1/collaboration/receipt?operation=accept", key, nil, 404)
}

func TestPostgresCollaborationFullProjectAndRecipientRetainRevocation(t *testing.T) {
	dsn := testDatabase(t)
	a, s := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	admin := deliveryAdmin(t, dsn)
	oc, mc, lc := authClient(t), authClient(t), authClient(t)
	owner := registerFixture(t, oc, s.URL, "full-owner@example.test")
	member := registerFixture(t, mc, s.URL, "full-member@example.test")
	later := registerFixture(t, lc, s.URL, "full-later@example.test")
	op, e := a.currentPrincipal(operationsRequest(t, oc, s))
	if e != nil {
		t.Fatal(e)
	}
	mp, e := a.currentPrincipal(operationsRequest(t, mc, s))
	if e != nil {
		t.Fatal(e)
	}
	lp, e := a.currentPrincipal(operationsRequest(t, lc, s))
	if e != nil {
		t.Fatal(e)
	}
	inv, _, e := a.mutateCollaboration(op, "invite", newID("invite"), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Role: "editor"})
	if e != nil {
		t.Fatal(e)
	}
	current, _, e := a.mutateCollaboration(mp, "accept", newID("accept"), collaborationMutation{Invitation: inv.InvitationID, ExpectedVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, e = admin.Exec(ctx, `INSERT INTO studio_users(id,email,name,password_hash) SELECT 'usr_'||md5('capacity-user-'||i), 'capacity-user-'||i||'@sample.test','Fixture',decode('00','hex') FROM generate_series(1,127) i;
	 INSERT INTO studio_workspaces(id,owner_id,name) SELECT 'ws_'||md5('capacity-workspace-'||i),'usr_'||md5('capacity-user-'||i),'Fixture' FROM generate_series(1,127) i;
	 INSERT INTO studio_projects(id,workspace_id,name) SELECT 'prj_'||md5('capacity-project-'||i),'ws_'||md5('capacity-workspace-'||i),'Fixture' FROM generate_series(1,127) i`)
	if e != nil {
		t.Fatal(e)
	}
	var expires time.Time
	if e = admin.QueryRow(ctx, "SELECT clock_timestamp()+interval '5 seconds'").Scan(&expires); e != nil {
		t.Fatal(e)
	}
	_, e = admin.Exec(ctx, `INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
	 SELECT $1,$2,'usr_'||md5('capacity-user-'||i),'pci_'||md5('project-fixture-'||i),$3,'viewer','PENDING',1,1,
	 CASE WHEN i=1 THEN $4::timestamptz ELSE clock_timestamp()+interval '1 hour' END FROM generate_series(1,63) i`,
		owner.Project.ID, owner.Workspace.ID, owner.User.ID, expires)
	if e != nil {
		t.Fatal(e)
	}
	_, e = admin.Exec(ctx, `INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
	 SELECT 'prj_'||md5('capacity-project-'||i),'ws_'||md5('capacity-workspace-'||i),$1,'pci_'||md5('recipient-fixture-'||i),'usr_'||md5('capacity-user-'||i),'viewer','PENDING',1,1,
	 CASE WHEN i=1 THEN $2::timestamptz ELSE clock_timestamp()+interval '1 hour' END FROM generate_series(1,127) i`, member.User.ID, expires)
	if e != nil {
		t.Fatal(e)
	}
	for _, request := range []struct {
		who             *principal
		project, target string
	}{{op, owner.Project.ID, later.User.ID}, {lp, later.Project.ID, member.User.ID}} {
		key := newID("invite")
		_, _, e = a.mutateCollaboration(request.who, "invite", key, collaborationMutation{Project: request.project, Target: request.target, Role: "viewer"})
		requireOperation(t, e, "COLLABORATION_LIMIT")
		_, e = a.collaborationReceipt(request.who, "invite", key)
		requireOperation(t, e, "COLLABORATION_NOT_FOUND")
	}
	for _, opName := range []string{"downgrade", "revoke"} {
		current, _, e = a.mutateCollaboration(op, opName, newID(opName), collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Invitation: inv.InvitationID, ExpectedVersion: current.Version})
		if e != nil {
			t.Fatal(opName, e)
		}
	}
	if current.State != "REVOKED" {
		t.Fatal(current)
	}
	// Both released slots can immediately be assigned to different parties.
	for _, req := range []struct {
		who             *principal
		project, target string
	}{{op, owner.Project.ID, later.User.ID}, {lp, later.Project.ID, member.User.ID}} {
		if _, _, e = a.mutateCollaboration(req.who, "invite", newID("invite"), collaborationMutation{Project: req.project, Target: req.target, Role: "viewer"}); e != nil {
			t.Fatal("revocation did not release live capacity", e)
		}
	}
	reinvite := collaborationMutation{Project: owner.Project.ID, Target: member.User.ID, Role: "viewer", ExpectedVersion: current.Version}
	retryKey := newID("invite")
	_, _, e = a.mutateCollaboration(op, "invite", retryKey, reinvite)
	requireOperation(t, e, "COLLABORATION_LIMIT")
	_, _, e = collaborationTestAccess(a, mp, owner.Project.ID, false)
	requireOperation(t, e, "COLLABORATION_NOT_FOUND")
	_, e = admin.Exec(ctx, `INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
	 SELECT $1,$2,'usr_'||md5('capacity-user-'||i),'pci_'||md5('history-'||i),$3,'viewer','PENDING',1,1,clock_timestamp()-interval '1 hour' FROM generate_series(80,96) i`, owner.Project.ID, owner.Workspace.ID, owner.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	route := "/api/v1/projects/" + owner.Project.ID + "/collaboration/members"
	data, _ := presentationRequest(t, oc, s.URL, "", "GET", route, "", nil, 200)
	page := decode[CollaborationPage](t, data)
	if len(page.Items) != 80 || page.Items[63].State != "PENDING" || page.Items[64].State != "EXPIRED" {
		t.Fatal("active relations were hidden by old history", len(page.Items))
	}
	data, _ = presentationRequest(t, oc, s.URL, "", "GET", route+"?target_user_id="+member.User.ID, "", nil, 200)
	exact := decode[CollaborationPage](t, data)
	if len(exact.Items) != 1 || exact.Items[0].State != "REVOKED" || exact.Items[0].Version != current.Version {
		t.Fatal("exact historical lookup", exact)
	}
	if _, e = admin.Exec(ctx, "SELECT pg_sleep(GREATEST(0,EXTRACT(epoch FROM ($1::timestamptz-clock_timestamp())))::double precision)", expires); e != nil {
		t.Fatal(e)
	}
	renewed, _, e := a.mutateCollaboration(op, "invite", retryKey, reinvite)
	if e != nil || renewed.State != "PENDING" || renewed.Round != 2 || renewed.InvitationID == inv.InvitationID {
		t.Fatal("expired slots failed to admit a new round", renewed, e)
	}
	_, _, e = collaborationTestAccess(a, mp, owner.Project.ID, false)
	requireOperation(t, e, "COLLABORATION_NOT_FOUND")
}
