package studio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresOrderIntentTwoAccountBrowser(t *testing.T) {
	if os.Getenv("ORDER_INTENT_BROWSER_TEST") != "1" {
		t.Skip("requires built workbench and local Chromium")
	}
	f := newWorkflowIntegrationFixture(t)
	catalog, err := filepath.Abs(filepath.Join("..", "..", "config", "order-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.OrderCatalogFile = catalog
	_, server := f.open(t)
	const buyerEmail, otherEmail = "order-buyer@example.test", "order-other@example.test"
	registerFixture(t, authClient(t), server.URL, buyerEmail)
	registerFixture(t, authClient(t), server.URL, otherEmail)
	output := os.Getenv("ORDER_INTENT_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	if !filepath.IsAbs(output) {
		t.Fatal("ORDER_INTENT_BROWSER_OUTPUT must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "apps/workbench/scripts/order-intent-browser-smoke.mjs")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "ORDER_INTENT_BROWSER_URL="+server.URL, "ORDER_INTENT_BROWSER_OUTPUT="+output,
		"ORDER_INTENT_BUYER_EMAIL="+buyerEmail, "ORDER_INTENT_BUYER_PASSWORD=fixture-password-123",
		"ORDER_INTENT_OTHER_EMAIL="+otherEmail, "ORDER_INTENT_OTHER_PASSWORD=fixture-password-123", "ORDER_INTENT_PRODUCT_ID=credit-basic")
	raw, err := cmd.CombinedOutput()
	if bytes.Contains(raw, []byte("fixture-password-123")) {
		t.Fatal("browser disclosed a fixture credential")
	}
	if err != nil {
		t.Fatalf("order intentions browser: %v\n%s", err, raw)
	}
	t.Log(strings.TrimSpace(string(raw)))
	conn := deliveryAdmin(t, f.cfg.DatabaseURL)
	if outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_journals") != 0 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_ledger_postings") != 0 ||
		outboxCount(t, conn, "SELECT count(*) FROM studio_tasks") != 0 {
		t.Fatal("an order intention created wallet movements or a provider task")
	}
}

func TestPostgresOrderIntentRestoreWithCatalogDisabled(t *testing.T) {
	if os.Getenv("TEST_POSTGRES_BIN_DIR") == "" {
		t.Skip("requires pg_dump and pg_restore")
	}
	f := newWorkflowIntegrationFixture(t)
	catalog, err := filepath.Abs(filepath.Join("..", "..", "config", "order-catalog.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.OrderCatalogFile = catalog
	app, server := f.open(t)
	const email = "historical-order@example.test"
	client := authClient(t)
	registerFixture(t, client, server.URL, email)
	const api = "/api/v1/order-intents"
	raw, _ := presentationRequest(t, client, server.URL, "", "GET", api+"/catalog", "", nil, 200)
	caps := decode[orderCatalogCapabilities](t, raw)
	if !caps.CanCreate || len(caps.Items) != 1 {
		t.Fatal("expected enabled isolated example catalog")
	}
	product := caps.Items[0]
	input := OrderIntentCreate{ProductID: product.ID, ProductDigest: product.ProductDigest, CatalogDigest: product.CatalogDigest}
	raw, _ = presentationRequest(t, client, server.URL, "", "POST", api, "order-backup-created", input, 201)
	order := decode[OrderIntent](t, raw)
	presentationRequest(t, client, server.URL, "", "POST", api+"/"+order.ID+"/cancel", "order-backup-cancelled", map[string]uint64{"expected_version": 1}, 200)
	raw, _ = presentationRequest(t, client, server.URL, "", "POST", api, "order-backup-still-pending", input, 201)
	pending := decode[OrderIntent](t, raw)
	server.Close()
	app.Close()
	restoreWorkflowFixture(t, f, "studio_order_intents", "studio_order_intent_receipts", "studio_ledger_journals", "studio_ledger_postings")
	f.cfg.OrderCatalogFile = ""
	_, restored := f.open(t)
	client = authClient(t)
	presentationRequest(t, client, restored.URL, "", "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "fixture-password-123"}, 200)
	raw, _ = presentationRequest(t, client, restored.URL, "", "GET", api+"/catalog", "", nil, 200)
	caps = decode[orderCatalogCapabilities](t, raw)
	if caps.CanCreate || !caps.CanManage {
		t.Fatal("disabled catalog must preserve own history management only")
	}
	for _, item := range []struct {
		path, key, id, state string
		version              uint64
	}{
		{api + "/receipt?operation=create", "order-backup-created", order.ID, "PENDING", 1},
		{api + "/receipt?operation=cancel", "order-backup-cancelled", order.ID, "CANCELLED", 2},
		{api + "/" + order.ID, "", order.ID, "CANCELLED", 2}, {api + "/" + pending.ID, "", pending.ID, "PENDING", 1},
	} {
		raw, _ = presentationRequest(t, client, restored.URL, "", "GET", item.path, item.key, nil, 200)
		v := decode[OrderIntent](t, raw)
		if v.ID != item.id || v.State != item.state || v.Version != item.version || v.Product != product {
			t.Fatal("restored order identity, original terms or receipt changed")
		}
	}
	presentationRequest(t, client, restored.URL, "", "POST", api+"/"+pending.ID+"/cancel", "order-cancel-after-restore", map[string]uint64{"expected_version": 1}, 200)
	database := deliveryAdmin(t, f.cfg.DatabaseURL)
	if outboxCount(t, database, "SELECT count(*) FROM studio_ledger_journals") != 0 || outboxCount(t, database, "SELECT count(*) FROM studio_ledger_postings") != 0 {
		t.Fatal("restoring or cancelling an unpaid intention changed the CREDIT ledger")
	}
}
