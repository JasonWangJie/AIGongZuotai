package studio

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresSupportTicketUpgrade033PreservesOrderAndOperatorRoles(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabase(t)
	c := deliveryAdmin(t, dsn)
	old := workflowOldMigrations(t, "033")
	tx, e := c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','existing');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','existing');
		INSERT INTO studio_users(id,email,name,password_hash) VALUES('usr_old','buyer@old.test','原申请人',decode('00','hex'));
		INSERT INTO studio_workspaces(id,owner_id,name) VALUES('ws_old','usr_old','原空间');
		INSERT INTO studio_operations_grants(id,user_id,workspace_id,role)
		  SELECT 'role_'||r,'usr_old','ws_old',r FROM unnest(ARRAY['observer','operator','finance','moderator','risk_reviewer']) r;
		WITH original AS (INSERT INTO studio_order_intents(id,workspace_id,owner_user_id,product_id,product_name,catalog_version,catalog_digest,
			product_digest,currency,currency_exponent,amount_minor,credit_micros,state,version)
			VALUES('ord_'||repeat('a',32),'ws_old','usr_old','old-product','原中文目录','v1',repeat('a',64),repeat('b',64),'CNY',2,300,7000000,'PENDING',1) RETURNING *)
		INSERT INTO studio_order_intent_receipts(owner_user_id,workspace_id,operation,key_hash,request_hash,order_id,snapshot)
		 SELECT owner_user_id,workspace_id,'create',decode(repeat('12',32),'hex'),decode(repeat('23',32),'hex'),id,to_jsonb(original) FROM original;`)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	tables := []string{"studio_order_intents", "studio_order_intent_receipts", "studio_operations_grants", "studio_users", "studio_workspaces", "studio_projects", "studio_ledger_journals"}
	before := schedulerOriginalSnapshot(t, c, tables)
	s, e := openPostgres(dsn)
	if e != nil {
		t.Fatal(e)
	}
	s.close()
	if !reflect.DeepEqual(before, schedulerOriginalSnapshot(t, c, tables)) {
		t.Fatal("034 changed 033 order, receipt, identity, grant or ledger")
	}
	if outboxCount(t, c, "SELECT (SELECT count(*) FROM studio_support_tickets)+(SELECT count(*) FROM studio_support_messages)+(SELECT count(*) FROM studio_support_receipts)+(SELECT count(*) FROM studio_operations_grants WHERE role='support')") != 0 {
		t.Fatal("upgrade invented support grants or tickets")
	}
	tx, e = c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = applyMigrationsFrom(ctx, tx, old); e == nil || !strings.Contains(e.Error(), "newer than this executable") {
		t.Fatal("033 writer accepted 034", e)
	}
}

func TestPostgresSupportTicketLostAcknowledgements(t *testing.T) {
	for _, op := range []string{"create", "followup", "reply", "close"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			f := newWorkflowIntegrationFixture(t)
			direct := f.cfg.DatabaseURL
			admin := deliveryAdmin(t, direct)
			proxy, armed, dropped := recoveryControlAckProxy(t, direct)
			f.cfg.DatabaseURL = proxy
			f.cfg.OperationsEnabled = true
			a, s := f.open(t)
			owner, staff := authClient(t), authClient(t)
			id := registerFixture(t, owner, s.URL, "support-ack-owner@example.test")
			p, e := a.currentPrincipal(operationsRequest(t, owner, s))
			if e != nil {
				t.Fatal(e)
			}
			in := SupportTicketCreate{"other", "确认单据", "第一次提交"}
			const api = "/api/v1/support/tickets"
			path := api
			client := owner
			receipt := api + "/receipt?operation=" + op
			var body any = in
			if op != "create" {
				ticket, _, e := a.createSupportTicket(p, newID("key"), in)
				if e != nil {
					t.Fatal(e)
				}
				path += "/" + ticket.ID + "/" + op
				body = map[string]any{"expected_version": 1}
				switch op {
				case "followup":
					body = SupportTicketAction{1, "追加说明"}
				case "reply":
					agent := registerFixture(t, staff, s.URL, "support-ack-agent@example.test")
					operationsGrantFixture(t, a, agent.User.ID, id.Workspace.ID, "support")
					path = "/api/v1/operations/support/tickets/" + ticket.ID + "/reply"
					client = staff
					receipt = "/api/v1/operations/support/tickets/receipt?operation=reply&workspace_id=" + id.Workspace.ID
					body = map[string]any{"expected_version": 1, "workspace_id": id.Workspace.ID, "body": "明确公开答复", "password": "fixture-password-123"}
				}
			}
			_, e = admin.Exec(ctx, `CREATE FUNCTION hold_support_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN PERFORM pg_advisory_xact_lock(912331,34); RETURN NEW; END $$;
				CREATE TRIGGER hold_support_receipt BEFORE INSERT ON studio_support_receipts FOR EACH ROW WHEN(NEW.operation='`+op+`') EXECUTE FUNCTION hold_support_receipt()`)
			if e != nil {
				t.Fatal(e)
			}
			barrier, e := admin.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer barrier.Rollback(ctx)
			if _, e = barrier.Exec(ctx, "SELECT pg_advisory_xact_lock(912331,34)"); e != nil {
				t.Fatal(e)
			}
			key := newID("key")
			done := make(chan int, 1)
			go func() { code, _, _ := authRequest(t, client, s.URL, "POST", path, key, "", body); done <- code }()
			verifyWorkflowLostAck(t, ctx, barrier, armed, dropped, done)
			s.Close()
			a.Close()
			f.cfg.DatabaseURL = direct
			_, again := f.open(t)
			raw, _ := presentationRequest(t, client, again.URL, "", "GET", receipt, key, nil, 200)
			got := decode[SupportTicketDetail](t, raw)
			wantBody := map[string]string{"create": in.Body, "followup": "追加说明", "reply": "明确公开答复", "close": ""}
			wantVersion := 2
			if op == "create" {
				wantVersion = 1
			}
			if len(got.Messages) != wantVersion || got.Version != uint64(wantVersion) || got.Messages[wantVersion-1].Body != wantBody[op] || !validOperationsID(got.ID, "tkt") {
				t.Fatal("wrong historic receipt after COMMIT loss")
			}
			if (got.State == "CLOSED") != (op == "close") || outboxCount(t, admin, "SELECT count(*) FROM studio_support_tickets") != 1 ||
				outboxCount(t, admin, "SELECT count(*) FROM studio_support_messages") != wantVersion || outboxCount(t, admin, "SELECT count(*) FROM studio_support_receipts WHERE operation=$1", op) != 1 {
				t.Fatal("lost or duplicated ticket, message or receipt")
			}
		})
	}
}
