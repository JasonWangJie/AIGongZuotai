package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresPresentationUpgrade028PreservesDraftAndQuote(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	conn := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "028")
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing development workspace');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing development project');
		INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_prior','prior@presentation.test','prior owner',decode('00','hex'));
		INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_prior','usr_prior','existing workspace');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_prior','ws_prior','existing project');
		INSERT INTO studio_project_drafts(project_id,workspace_id,revision,updated_at) VALUES('prj_prior','ws_prior',1,now());
		INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at)
		VALUES('prj_prior','ws_prior',1,'[{"id":"card","title":"原有标题","note":"原有正文及标点。"}]',1,now());
		INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at)
		VALUES('prj_prior','ws_prior','draft-original-key',decode(repeat('ab',32),'hex'),1,now());
		INSERT INTO studio_ledger_quotes(id,workspace_id,project_id,owner_user_id,resource_type,model_id,model_version,price_version,request_hash,price,reserve_micros,expires_at)
		VALUES('quo_prior','ws_prior','prj_prior','usr_prior','task','studio-image','model-old','price-old',decode(repeat('ac',32),'hex'),'{"artifact_micros":"200000","kind":"image"}',200000,now()+interval '10 minutes');
		INSERT INTO studio_credit_risk_policies(workspace_id,version,enabled,max_held_micros,max_per_minute) VALUES('ws_prior',1,true,600000,12);
		INSERT INTO studio_operations_grants(id,user_id,workspace_id,role) VALUES('grant_prior','usr_prior','ws_prior','risk_reviewer')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tables := []string{"studio_users", "studio_workspaces", "studio_projects", "studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency",
		"studio_ledger_quotes", "studio_credit_risk_policies", "studio_operations_grants", "studio_account_governance_grants", "studio_credit_risk_permits", "studio_ledger_reservations"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	store, err := openPostgres(dsn)
	if err != nil {
		t.Fatal(err)
	}
	store.close()
	if after := schedulerOriginalSnapshot(t, conn, tables); !reflect.DeepEqual(before, after) {
		t.Fatal("029 changed pre-existing creation, financial, or authorization data")
	}
	if n := outboxCount(t, conn, `SELECT (SELECT count(*) FROM studio_presentations)+(SELECT count(*) FROM studio_presentation_versions)
		+(SELECT count(*) FROM studio_presentation_assets)+(SELECT count(*) FROM studio_presentation_keys)`); n != 0 {
		t.Fatal("029 invented presentation content, original bindings or receipts")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("028 writer accepted 029", err)
	}
}

func presentationTestDraft(title string) PresentationSaveInput {
	n := uint64(0)
	return PresentationSaveInput{ExpectedVersion: &n, Document: PresentationDocument{Title: title, Theme: "paper", Slides: []PresentationSlide{{ID: "one", Layout: "content", Title: "第一页", Body: "第一行\n第二行", Notes: "仅供讲者"}}}}
}
func TestPresentationStoreDocumentLimitsAndReadOnlyFields(t *testing.T) {
	in := presentationTestDraft("说明")
	if e := validatePresentation(in); e != nil {
		t.Fatal(e)
	}
	cases := []func(*PresentationSaveInput){
		func(x *PresentationSaveInput) { x.Document.Slides = nil },
		func(x *PresentationSaveInput) { x.Document.Theme = "css:private" },
		func(x *PresentationSaveInput) { x.Document.Slides = append(x.Document.Slides, x.Document.Slides[0]) },
		func(x *PresentationSaveInput) { x.Document.Slides[0].Notes = strings.Repeat("图", 2001) },
		func(x *PresentationSaveInput) { x.Document.Slides[0].ContentVersion = strings.Repeat("0", 64) },
		func(x *PresentationSaveInput) { x.Document.Slides[0].Body = "x\u0001y" },
		func(x *PresentationSaveInput) { x.Document.Slides[0].Layout = "image" },
		func(x *PresentationSaveInput) {
			for i := 0; i < 30; i++ {
				x.Document.Slides = append(x.Document.Slides, PresentationSlide{ID: fmt.Sprintf("s%d", i), Title: "页", Layout: "content"})
			}
		},
		func(x *PresentationSaveInput) {
			x.Document.Slides = nil
			for i := 0; i < 30; i++ {
				x.Document.Slides = append(x.Document.Slides, PresentationSlide{ID: fmt.Sprintf("s%d", i), Title: "页", Layout: "content", Body: strings.Repeat("汉", 4000), Notes: strings.Repeat("字", 2000)})
			}
		},
		func(x *PresentationSaveInput) {
			x.Document.Slides = nil
			for i := 0; i < 5; i++ {
				x.Document.Slides = append(x.Document.Slides, PresentationSlide{ID: fmt.Sprintf("s%d", i), Title: "页", Layout: "image", AssetID: libraryID("reference", "source", ""), AssetRevision: 1})
			}
		},
	}
	for i, fn := range cases {
		x := presentationTestDraft("说明")
		fn(&x)
		if validatePresentation(x) == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
}

// Include multi-byte and JSON-escaped content; every slide remains within
// its individual character budget on both sides of the document byte limit.
func presentationBoundaryDraft(t *testing.T) PresentationSaveInput {
	t.Helper()
	in := presentationTestDraft("可编辑边界")
	in.Document.Slides = nil
	for i := 0; i < 30; i++ {
		in.Document.Slides = append(in.Document.Slides, PresentationSlide{
			ID: fmt.Sprintf("page_%d", i), Title: "页", Layout: "content", Notes: "引号\"与制表\t和换行\n",
		})
	}
	start, err := json.Marshal(in.Document)
	if err != nil {
		t.Fatal(err)
	}
	remaining := presentationMaxDocumentBytes - len(start)
	for i := range in.Document.Slides {
		n := min(remaining/3, 3998)
		in.Document.Slides[i].Body = strings.Repeat("文", n)
		remaining -= 3 * n
		if remaining < 3 {
			in.Document.Slides[i].Body += strings.Repeat("x", remaining)
			break
		}
	}
	raw, err := json.Marshal(in.Document)
	if err != nil || len(raw) != presentationMaxDocumentBytes {
		t.Fatalf("boundary size %d: %v", len(raw), err)
	}
	return in
}

func TestPresentationStoreNormalizedJSONAtByteLimit(t *testing.T) {
	in := presentationBoundaryDraft(t)
	if err := validatePresentation(in); err != nil {
		t.Fatal(err)
	}
	in.Document.Slides[29].Body += "x"
	requireOperation(t, validatePresentation(in), "PRESENTATION_TOO_LARGE")
}

func TestPostgresPresentationCapacityStillReadsAndReplays(t *testing.T) {
	dsn := testDatabase(t)
	app, server := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "presentation-capacity@example.test")
	p, err := app.currentPrincipal(operationsRequest(t, client, server))
	if err != nil {
		t.Fatal(err)
	}
	project := identity.Project.ID
	original, key := presentationBoundaryDraft(t), newID("save")
	first, _, err := app.savePresentation(p, project, original, key)
	if err != nil {
		t.Fatal(err)
	}
	oversized := presentationBoundaryDraft(t)
	oversized.Document.Slides[29].Body += "x"
	_, _, err = app.savePresentation(p, project, oversized, newID("oversize"))
	requireOperation(t, err, "PRESENTATION_TOO_LARGE")
	edit := presentationTestDraft("小修订")
	edit.PresentationID = first.ID
	for version := uint64(1); version < presentationVersionLimit; version++ {
		edit.ExpectedVersion = &version
		if _, _, err = app.savePresentation(p, project, edit, newID("edit")); err != nil {
			t.Fatalf("save after version %d: %v", version, err)
		}
	}
	full := uint64(presentationVersionLimit)
	edit.ExpectedVersion = &full
	if _, _, err = app.savePresentation(p, project, edit, newID("full")); err == nil {
		t.Fatal("version limit was bypassed")
	}
	requireOperation(t, err, "PRESENTATION_VERSION_LIMIT")
	last, err := app.presentationVersion(p, project, first.ID, full, true)
	if err != nil || last.Version != full {
		t.Fatal("latest version missing at capacity", err)
	}
	got, replayed, err := app.savePresentation(p, project, original, key)
	if err != nil || !replayed || got.Version != 1 || len(got.Document.Slides) != 30 {
		t.Fatal("original at capacity", err)
	}
}
func TestPostgresPresentationVersionsScopeAndFrozenAsset(t *testing.T) {
	dsn := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required", AssetDir: t.TempDir()})
	admin := deliveryAdmin(t, dsn)
	owner, other := authClient(t), authClient(t)
	id := registerFixture(t, owner, server.URL, "presentation-owner@example.test")
	peer := registerFixture(t, other, server.URL, "presentation-second@example.test")
	p, e := a.currentPrincipal(operationsRequest(t, owner, server))
	if e != nil {
		t.Fatal(e)
	}
	pn, e := a.currentPrincipal(operationsRequest(t, other, server))
	if e != nil {
		t.Fatal(e)
	}
	blob := referencePNG(t, 72)
	code, raw, _ := uploadReferenceFixture(t, owner, server.URL, id.Project.ID, newID("refkey"), "slide.png", blob, false)
	if code != 201 {
		t.Fatal("reference original not created", code)
	}
	ref := decode[ReferenceAsset](t, raw)
	asset := libraryID("reference", ref.ID, "")
	sum := sha256.Sum256(blob)
	draft := presentationTestDraft("原版")
	draft.Document.Slides[0].Layout = "image"
	draft.Document.Slides[0].AssetID = asset
	draft.Document.Slides[0].AssetRevision = 1
	key := newID("save")
	first, rep, e := a.savePresentation(p, id.Project.ID, draft, key)
	if e != nil || rep || first.Version != 1 || first.Document.Slides[0].ContentVersion != hex.EncodeToString(sum[:]) {
		t.Fatal("save original", first, e)
	}
	source, e := a.presentationImage(p, id.Project.ID, first.ID, 1, "one")
	if e != nil || source.ReferenceBlob == nil || source.ByteSize != int64(len(blob)) || source.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("original asset source", e)
	}
	_, e = a.presentationVersion(pn, peer.Project.ID, first.ID, 1, false)
	requireOperation(t, e, "PRESENTATION_NOT_FOUND")
	_, e = a.presentationImage(pn, id.Project.ID, first.ID, 1, "one")
	requireOperation(t, e, "PRESENTATION_NOT_FOUND")
	_, _, e = a.savePresentation(pn, peer.Project.ID, draft, newID("foreign"))
	requireOperation(t, e, "PRESENTATION_ASSET_UNAVAILABLE")
	_, e = a.presentationReceipt(pn, peer.Project.ID, key)
	requireOperation(t, e, "PRESENTATION_NOT_FOUND")
	// Rename creates a real library revision. Old saved bytes remain eligible.
	code, _, _ = authRequest(t, owner, server.URL, "PATCH", "/api/v1/library/"+asset, newID("rename"), id.Project.ID, metadataInput(1, "renamed", []string{}))
	if code != 200 {
		t.Fatal("rename", code)
	}
	now, e := a.presentationVersion(p, id.Project.ID, first.ID, 1, true)
	if e != nil || now.Document.Slides[0].AssetRevision != 1 {
		t.Fatal("metadata revision rewrote old presentation", e)
	}
	edit := presentationTestDraft("改版")
	edit.PresentationID = first.ID
	v := uint64(1)
	edit.ExpectedVersion = &v
	edit.Document.Slides[0] = draft.Document.Slides[0]
	_, _, e = a.savePresentation(p, id.Project.ID, edit, newID("stale"))
	requireOperation(t, e, "PRESENTATION_ASSET_UNAVAILABLE")
	edit.Document.Slides[0].AssetRevision = 2
	second, _, e := a.savePresentation(p, id.Project.ID, edit, newID("edit"))
	if e != nil || second.Version != 2 {
		t.Fatal("second version", e)
	}
	_, _, e = a.savePresentation(p, id.Project.ID, edit, newID("stale"))
	requireOperation(t, e, "PRESENTATION_VERSION_CONFLICT")
	code, _, _ = authRequest(t, owner, server.URL, "POST", "/api/v1/library/"+asset+"/trash", newID("trash"), id.Project.ID, stateInput(2))
	if code != 200 {
		t.Fatal("trash", code)
	}
	history, e := a.presentationVersion(p, id.Project.ID, first.ID, 1, false)
	if e != nil || history.Document.Title != "原版" || history.Document.Slides[0].AssetAvailable == nil || *history.Document.Slides[0].AssetAvailable {
		t.Fatal("unavailable image lost historical text", e)
	}
	_, e = a.presentationVersion(p, id.Project.ID, first.ID, 1, true)
	requireOperation(t, e, "PRESENTATION_ASSET_UNAVAILABLE")
	_, e = a.presentationImage(p, id.Project.ID, first.ID, 2, "one")
	requireOperation(t, e, "PRESENTATION_ASSET_UNAVAILABLE")
	got, rep, e := a.savePresentation(p, id.Project.ID, draft, key)
	if e != nil || !rep || got.Version != 1 || got.ID != first.ID || *got.Document.Slides[0].AssetAvailable {
		t.Fatal("old key replay reselected a blocked source", e)
	}
	alt := draft
	alt.Document.Title = "不同"
	_, _, e = a.savePresentation(p, id.Project.ID, alt, key)
	requireOperation(t, e, "IDEMPOTENCY_CONFLICT")
	page, e := a.presentationHistory(p, id.Project.ID, first.ID, 0, 1)
	if e != nil || len(page.Items) != 1 || page.Items[0].Version != 2 || page.NextBeforeVersion == nil {
		t.Fatal(page, e)
	}
	prior, e := a.presentationHistory(p, id.Project.ID, first.ID, *page.NextBeforeVersion, 1)
	if e != nil || len(prior.Items) != 1 || prior.Items[0].Version != 1 || prior.NextBeforeVersion != nil {
		t.Fatal(prior, e)
	}
	code, _, _ = authRequest(t, owner, server.URL, "POST", "/api/v1/library/"+asset+"/restore", newID("restore"), id.Project.ID, stateInput(3))
	if code != 200 {
		t.Fatal("restore", code)
	}
	if _, e = a.presentationVersion(p, id.Project.ID, first.ID, 1, true); e != nil {
		t.Fatal("restored original is unavailable", e)
	}
	if _, e = admin.Exec(context.Background(), "UPDATE studio_references SET blob=jsonb_set(blob,'{sha256}',to_jsonb($1::text)) WHERE id=$2", strings.Repeat("0", 64), ref.ID); e != nil {
		t.Fatal(e)
	}
	_, e = a.presentationVersion(p, id.Project.ID, first.ID, 1, true)
	requireOperation(t, e, "PRESENTATION_ASSET_UNAVAILABLE")
	if _, e = admin.Exec(context.Background(), `UPDATE studio_presentation_versions SET document='{}' WHERE presentation_id=$1 AND version=1`, first.ID); e == nil {
		t.Fatal("immutable version could be changed")
	}
}
func TestPostgresPresentationIndependentWritersCASAndReceipts(t *testing.T) {
	dsn := testDatabase(t)
	a, server := testApp(t, Config{DatabaseURL: dsn, AuthMode: "required"})
	admin := deliveryAdmin(t, dsn)
	ctx := context.Background()
	client := authClient(t)
	identity := registerFixture(t, client, server.URL, "presentation-cas@example.test")
	p, e := a.currentPrincipal(operationsRequest(t, client, server))
	if e != nil {
		t.Fatal(e)
	}
	original := presentationTestDraft("原稿")
	originalKey := newID("first")
	v, _, e := a.savePresentation(p, identity.Project.ID, original, originalKey)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := admin.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT id FROM studio_users WHERE id=$1 FOR UPDATE", p.User.ID); e != nil {
		t.Fatal(e)
	}
	type result struct {
		key string
		err error
	}
	done := make(chan result, 2)
	pids := []int32{}
	for i := 0; i < 2; i++ {
		conn := deliveryAdmin(t, dsn)
		pids = append(pids, int32(conn.PgConn().PID()))
		writer := &App{config: Config{AuthMode: "required"}, store: &postgresStore{conn: conn}}
		edit := presentationTestDraft(fmt.Sprintf("并发保存%d", i))
		edit.ExpectedVersion = &v.Version
		edit.PresentationID = v.ID
		k := newID("cas")
		go func() { _, _, e := writer.savePresentation(p, identity.Project.ID, edit, k); done <- result{k, e} }()
	}
	awaitRunLeaseWaiters(t, deliveryAdmin(t, dsn), pids...)
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	won := 0
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err == nil {
			won++
			v, e := a.presentationReceipt(p, identity.Project.ID, r.key)
			if e != nil || v.Version != 2 {
				t.Fatal("successful writer receipt", e)
			}
		} else {
			requireOperation(t, r.err, "PRESENTATION_VERSION_CONFLICT")
			_, e = a.presentationReceipt(p, identity.Project.ID, r.key)
			requireOperation(t, e, "PRESENTATION_NOT_FOUND")
		}
	}
	if won != 1 || outboxCount(t, admin, "SELECT count(*) FROM studio_presentation_versions WHERE presentation_id=$1", v.ID) != 2 {
		t.Fatal("CAS did not serialize independent writers")
	}
	list, e := a.listPresentations(p, identity.Project.ID, "", 1)
	if e != nil || len(list.Items) != 1 || list.Items[0].Version != 2 {
		t.Fatal(list, e)
	}
	got, rep, e := a.savePresentation(p, identity.Project.ID, original, originalKey)
	if e != nil || !rep || got.Version != 1 {
		t.Fatal("original retry advanced head", e)
	}
}
