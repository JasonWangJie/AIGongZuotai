package studio

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresMalformedReferenceQuoteDoesNotTripDatabaseFuse(t *testing.T) {
	a, server, client, identity := billingHTTPFixture(t)
	in := taskInput("image")
	in.ProjectID = identity.Project.ID
	in.Operation = "edit"
	for _, id := range []string{"ref_\x00", "ref_" + strings.Repeat("a", 31) + "\x00", "ref_" + strings.Repeat("a", 33), "ref_中文", "not-a-reference"} {
		in.Inputs.ReferenceAssetIDs = []string{id}
		code, data, _ := authRequest(t, client, server.URL, "POST", "/api/v1/billing/quotes", "", identity.Project.ID, map[string]any{"resource_type": "task", "task_input": in})
		if code != 422 || !strings.Contains(string(data), "INVALID_REFERENCES") {
			t.Fatalf("invalid quote ID reached storage: %d %s", code, data)
		}
		code, _, _ = authRequest(t, client, server.URL, "GET", "/api/v1/references/"+url.PathEscape(id)+"/content", "", identity.Project.ID, nil)
		if code != 404 {
			t.Fatalf("invalid direct reference ID: %d", code)
		}
		if a.store.failed.Load() || a.ctx.Err() != nil {
			t.Fatal("malformed user input tripped database fuse")
		}
	}
	code, _, _ := authRequest(t, client, server.URL, "GET", "/readyz", "", "", nil)
	if code != 200 {
		t.Fatal("service no longer ready")
	}
	code, _, _ = authRequest(t, client, server.URL, "GET", "/api/v1/billing/wallet", "", identity.Project.ID, nil)
	if code != 200 {
		t.Fatal("valid wallet request failed after malformed input")
	}
	var writes int
	if err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		return c.QueryRow(ctx, "SELECT (SELECT count(*) FROM studio_ledger_quotes)+(SELECT count(*) FROM studio_ledger_reservations)+(SELECT count(*) FROM studio_tasks)").Scan(&writes)
	}); err != nil || writes != 0 {
		t.Fatal("malformed quote wrote business state", writes, err)
	}
}
