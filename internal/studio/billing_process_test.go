package studio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresBillingActualCrashHoldsAndReplay(t *testing.T) {
	database := testDatabase(t)
	var submits atomic.Int32
	received := make(chan struct{}, 2)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || !payload.StreamOptions.IncludeUsage {
			http.Error(w, "metered stream required", 400)
			return
		}
		submits.Add(1)
		received <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"持久计费中断测试\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer gateway.Close()
	pricePath := filepath.Join(t.TempDir(), "live-fixture-prices.json")
	prices := `{"schema_version":1,"provider_mode":"live","unit":"CREDIT","scale":6,"rates":[{"model_id":"studio-chat","kind":"chat","operation":"chat","input_per_million_micros":"1000000","output_per_million_micros":"4000000"}]}`
	if err := os.WriteFile(pricePath, []byte(prices), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := durableProcessConfig{DatabaseURL: database, ReferenceDir: filepath.Join(t.TempDir(), "refs"), AssetDir: filepath.Join(t.TempDir(), "assets"), GatewayURL: gateway.URL, ChatGatewayURL: gateway.URL + "/v1", BillingPriceFile: pricePath}
	first := startDurableChild(t, fixture)
	client := authClient(t)
	identity := registerFixture(t, client, first.base, "billing-crash@example.test")
	first.kill(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grant := LedgerAdminConfig{DatabaseURL: database, Action: "grant", Credit: LedgerCreditRequest{WorkspaceID: identity.Workspace.ID, AmountMicros: 1000000, OperationKey: "process-billing-grant", Reason: "fixture", EvidenceRef: "fixture:process"}}
	plan, err := AdministerLedger(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Apply = true
	grant.PlanDigest = plan.PlanDigest
	if _, err = AdministerLedger(ctx, grant); err != nil {
		t.Fatal(err)
	}
	second := startDurableChild(t, fixture)
	input := map[string]any{"project_id": identity.Project.ID, "model_id": "studio-chat", "input": "实际进程中断后不可重复生成或释放未知成本"}
	code, data, _ := authRequest(t, client, second.base, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, map[string]any{"resource_type": "run", "run_input": input})
	if code != 201 {
		t.Fatalf("quote %d %s", code, data)
	}
	var quote struct {
		ID            string `json:"id"`
		ReserveMicros int64  `json:"reserve_micros,string"`
	}
	if json.Unmarshal(data, &quote) != nil || quote.ReserveMicros <= 0 {
		t.Fatal("invalid quote")
	}
	input["quote_id"] = quote.ID
	code, data, _ = authRequest(t, client, second.base, "POST", "/api/v1/runs", "process-metered-run", identity.Project.ID, input)
	if code != 201 {
		t.Fatalf("run %d %s", code, data)
	}
	run := decode[Run](t, data)
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not reached")
	}
	second.kill(t)
	third := startDurableChild(t, fixture)
	code, data, _ = authRequest(t, client, third.base, "GET", "/api/v1/runs/"+run.ID+"/snapshot", "", identity.Project.ID, nil)
	recovered := decode[Run](t, data)
	if code != 200 || recovered.Status != "INTERRUPTED" || recovered.Billing == nil || recovered.Billing.State != "REVIEW" || recovered.Billing.ChargedMicros != 0 || recovered.Billing.ReleasedMicros != 0 {
		t.Fatalf("unsafe recovery: %d %s", code, data)
	}
	code, data, headers := authRequest(t, client, third.base, "POST", "/api/v1/runs", "process-metered-run", identity.Project.ID, input)
	if code != 201 || decode[Run](t, data).ID != run.ID || headers.Get("Idempotency-Replayed") != "true" {
		t.Fatal("original replay failed")
	}
	code, data, _ = authRequest(t, client, third.base, "GET", "/api/v1/billing/wallet", "", identity.Project.ID, nil)
	wallet := decode[LedgerWallet](t, data)
	if code != 200 || wallet.ReservedMicros != quote.ReserveMicros || wallet.ReviewMicros != quote.ReserveMicros || wallet.AvailableMicros != 1000000-quote.ReserveMicros || submits.Load() != 1 {
		t.Fatal("hold or provider submit mismatch", wallet, submits.Load())
	}
	third.kill(t)
	inspect, err := AdministerLedger(ctx, LedgerAdminConfig{DatabaseURL: database, Action: "inspect", WorkspaceID: identity.Workspace.ID})
	if err != nil || inspect.Reconciliation.JournalCount != 2 {
		t.Fatal("recovery ledger imbalance", inspect, err)
	}
	resolve := LedgerAdminConfig{DatabaseURL: database, Action: "resolve", Resolve: LedgerResolveRequest{ReservationID: recovered.Billing.ReservationID, OperationKey: "process-manual-release", Reason: "verified-no-delivery", EvidenceRef: "fixture:crash-review", Decision: "release"}}
	plan, err = AdministerLedger(ctx, resolve)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Apply = true
	resolve.PlanDigest = plan.PlanDigest
	released, err := AdministerLedger(ctx, resolve)
	if err != nil || released.Result.Wallet.ReservedMicros != 0 || released.Result.Wallet.AvailableMicros != 1000000 {
		t.Fatal("explicit review release failed", released, err)
	}
	t.Log("Actual process kill after one metered upstream POST: restart preserves REVIEW hold, original key replays without another POST; offline evidence release balances the ledger.")
}
