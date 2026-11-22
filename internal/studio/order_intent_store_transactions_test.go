package studio

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresOrderIntentUpgrade032PreservesMemberAndDraftEvidence(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	conn := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "032")
	tx, e := conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing demo');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing project');
	 INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_host','host@order.test','Owner',decode('00','hex')),('usr_member','member@order.test','Editor',decode('00','hex'));
	 INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_before','usr_host','宿主空间'),('ws_member','usr_member','个人空间');
	 INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_before','ws_before','原共享项目'),('prj_member','ws_member','个人项目');
	 INSERT INTO studio_project_drafts(project_id,workspace_id,revision,updated_at) VALUES('prj_before','ws_before',2,clock_timestamp());
	 INSERT INTO studio_draft_revisions(project_id,workspace_id,revision,nodes,node_count,created_at) VALUES
	 ('prj_before','ws_before',1,'[{"id":"card","title":"初稿","note":"所有者"}]',1,clock_timestamp()),
	 ('prj_before','ws_before',2,'[{"id":"card","title":"编辑稿","note":"成员改动"}]',1,clock_timestamp());
	 INSERT INTO studio_draft_idempotency(project_id,workspace_id,idempotency_key,payload_hash,revision,created_at)
	 VALUES('prj_before','ws_before','legacy-author-key',decode(repeat('ac',32),'hex'),1,clock_timestamp());
	 INSERT INTO studio_collaboration_members(project_id,workspace_id,target_user_id,invitation_id,inviter_id,role,state,round,version,expires_at)
	 VALUES('prj_before','ws_before','usr_member','pci_'||repeat('b',32),'usr_host','editor','PENDING',1,1,clock_timestamp()+interval '72 hours');
	 INSERT INTO studio_collaboration_receipts(actor_id,operation,key_hash,request_hash,project_id,target_user_id,version,response) VALUES
	 ('usr_host','invite',decode(repeat('bc',32),'hex'),decode(repeat('bd',32),'hex'),'prj_before','usr_member',1,jsonb_build_object('operation','invite','project_id','prj_before','target_user_id','usr_member','invitation_id','pci_'||repeat('b',32),'round',1,'version',1,'role','editor','state','PENDING'));
	 UPDATE studio_collaboration_members SET state='ACTIVE',version=2 WHERE target_user_id='usr_member';
	 INSERT INTO studio_collaboration_receipts(actor_id,operation,key_hash,request_hash,project_id,target_user_id,version,response) VALUES
	 ('usr_member','accept',decode(repeat('be',32),'hex'),decode(repeat('bf',32),'hex'),'prj_before','usr_member',2,jsonb_build_object('operation','accept','project_id','prj_before','target_user_id','usr_member','invitation_id','pci_'||repeat('b',32),'round',1,'version',2,'role','editor','state','ACTIVE'));
	 INSERT INTO studio_collaboration_draft_keys(workspace_id,project_id,actor_id,key_hash,request_hash,revision)
	 VALUES('ws_before','prj_before','usr_member',sha256(convert_to('legacy-author-key','UTF8')),decode(repeat('cd',32),'hex'),2)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_collaboration_members", "studio_collaboration_receipts", "studio_collaboration_draft_keys", "studio_project_drafts", "studio_draft_revisions", "studio_draft_idempotency", "studio_users", "studio_workspaces", "studio_projects", "studio_sessions", "studio_ledger_wallets", "studio_ledger_accounts", "studio_ledger_journals", "studio_ledger_postings"}
	before := schedulerOriginalSnapshot(t, conn, tables)
	store, e := openPostgres(dsn)
	if e != nil {
		t.Fatal(e)
	}
	store.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, conn, tables)) {
		t.Fatal("033 changed pre-existing membership, author receipt, draft or financial evidence")
	}
	if n := outboxCount(t, conn, "SELECT (SELECT count(*) FROM studio_order_intents)+(SELECT count(*) FROM studio_order_intent_receipts)"); n != 0 {
		t.Fatal("upgrade invented an order")
	}
	tx, e = conn.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("032 writer accepted 033", e)
	}
}

func TestPostgresOrderIntentLostCreateAndCancelAcknowledgements(t *testing.T) {
	for _, op := range []string{"create", "cancel"} {
		t.Run(op, func(t *testing.T) {
			f := newWorkflowIntegrationFixture(t)
			direct := f.cfg.DatabaseURL
			admin := deliveryAdmin(t, direct)
			proxy, armed, dropped := recoveryControlAckProxy(t, direct)
			f.cfg.DatabaseURL = proxy
			f.cfg.OrderCatalogFile = filepath.Join("..", "..", "config", "order-catalog.example.json")
			a, s := f.open(t)
			c := authClient(t)
			registerFixture(t, c, s.URL, "order-ack-"+op+"@example.test")
			p, e := a.currentPrincipal(operationsRequest(t, c, s))
			if e != nil {
				t.Fatal(e)
			}
			create := orderStoreInput(a)
			product := a.orderCatalog.products[0]
			const api = "/api/v1/order-intents"
			path := api
			var body any = create
			if op == "cancel" {
				first, _, e := a.createOrderIntent(p, create, newID("create"))
				if e != nil {
					t.Fatal(e)
				}
				path += "/" + first.ID + "/cancel"
				body = map[string]uint64{"expected_version": 1}
			}
			ctx := context.Background()
			q := `CREATE FUNCTION hold_order_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
			 BEGIN PERFORM pg_advisory_xact_lock(912331,33); RETURN NEW; END $$;
			 CREATE TRIGGER hold_order_receipt BEFORE INSERT ON studio_order_intent_receipts FOR EACH ROW WHEN(NEW.operation='` + op + `') EXECUTE FUNCTION hold_order_receipt()`
			if _, e = admin.Exec(ctx, q); e != nil {
				t.Fatal(e)
			}
			barrier, e := admin.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer barrier.Rollback(ctx)
			if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912331,33)"); e != nil {
				t.Fatal(e)
			}
			key := newID(op)
			done := make(chan int, 1)
			go func() { status, _, _ := authRequest(t, c, s.URL, "POST", path, key, "", body); done <- status }()
			verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, done)
			s.Close()
			a.Close()
			f.cfg.DatabaseURL = direct
			f.cfg.OrderCatalogFile = ""
			_, again := f.open(t)
			raw, _ := presentationRequest(t, c, again.URL, "", "GET", api+"/receipt?operation="+op, key, nil, 200)
			got := decode[OrderIntent](t, raw)
			want := "PENDING"
			var v uint64 = 1
			if op == "cancel" {
				want = "CANCELLED"
				v = 2
			}
			if got.State != want || got.Version != v || got.Product != product || !validOperationsID(got.ID, "ord") {
				t.Fatal("wrong original snapshot after lost acknowledgement")
			}
			raw, _ = presentationRequest(t, c, again.URL, "", "GET", api+"/"+got.ID, "", nil, 200)
			if now := decode[OrderIntent](t, raw); now.State != want || now.Product != product ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_order_intents") != 1 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_order_intent_receipts WHERE operation=$1", op) != 1 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_ledger_journals") != 0 {
				t.Fatal("order, receipt or ledger changed during recovery")
			}
		})
	}
}
