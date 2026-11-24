package studio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"
)

// Keep the driver and the database observer independent. A delayed sample
// cannot hold the actual admission request schedule. The observer uses its own
// connection; a late sampling tick is skipped, never executed back-to-back.
type openLoopWatch259 struct {
	ticks, skips int
	maxDelay     time.Duration
}

func (w *openLoopWatch259) loop(ctx context.Context, start time.Time, p openLoopProfile259, sample func(context.Context) error, done <-chan struct{}) error {
	next := start
	for {
		if !openLoopWait259(ctx, next) {
			return errors.New("durable observer reached its deadline")
		}
		at := time.Now()
		w.maxDelay = max(w.maxDelay, at.Sub(next))
		if err := sample(ctx); err != nil {
			return err
		}
		w.ticks++
		select {
		case <-done:
			return nil
		default:
		}
		next = next.Add(p.Sample)
		for !next.After(time.Now()) {
			next = next.Add(p.Sample)
			w.skips++
		}
	}
}

func openLoopUsers259(t *testing.T, ctx context.Context, base string) []admissionCapacityAccount {
	t.Helper()
	users := admissionCapacityRegister(t, ctx, base, 2)
	for i := range users {
		expected := users[i].identity
		users[i] = admissionCapacityAccount{client: authClient(t)}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		users[i].client.Transport = tr
		users[i].client.Timeout = 8 * time.Second
		t.Cleanup(tr.CloseIdleConnections)
		users[i].request(t, ctx, base, "POST", "/api/v1/auth/login", "", map[string]string{"email": expected.User.Email, "password": "test-only-capacity-246"}, &users[i].identity, 200)
		got := users[i].identity
		if got.Workspace.ID != expected.Workspace.ID || got.Project.ID != expected.Project.ID || got.User.ID != expected.User.ID {
			t.Fatal("fresh login did not return the original project")
		}
	}
	if users[0].identity.Workspace.ID == users[1].identity.Workspace.ID {
		t.Fatal("distinct workspaces required")
	}
	return users
}

func TestPostgresOpenLoopMediaLifecycle259(t *testing.T) {
	name := os.Getenv("STUDIO_MEDIA_OPENLOOP")
	if name == "" {
		t.Skip("explicit 259 fixed-rate media lifecycle workload")
	}
	p, ok := mediaOpenLoopProfile259(name)
	if !ok || !admissionCapacityLocalDSN(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("select pilot or 1ps and a private loopback PostgreSQL URL")
	}
	fault := os.Getenv("STUDIO_MEDIA_OPENLOOP_FAULT")
	if fault != "" && fault != "one429" {
		t.Fatal("fault profile must be empty or one429")
	}
	report := openOpenLoopReport259(t, os.Getenv("STUDIO_MEDIA_OPENLOOP_REPORT"))
	dsn := testDatabase(t)
	admin := deliveryAdmin(t, dsn)
	observer := deliveryAdmin(t, dsn)
	gateway := newOpenLoopGateway259(p, fault == "one429")
	upstream := httptest.NewServer(gateway)
	t.Cleanup(upstream.Close)
	cfg := largeQueryConfig040(dsn, upstream.URL, 20*time.Millisecond)
	cfg.AuthMode = "required"
	cfg.AssetDir, cfg.ReferenceDir = t.TempDir(), t.TempDir()
	cfg.AsyncImageModel = openLoopModel259
	cfg.AsyncImageCredentialID = "fixture259"
	cfg.MediaScheduling.QueryWorkers = 64
	cfg.MediaScheduling.SubmitWorkers = 2
	cfg.MediaScheduling.WorkspaceConcurrency = 2
	cfg.MediaScheduling.ChannelConcurrency = 2
	cfg.MediaScheduling.QueryWorkspaceConcurrency = 32
	cfg.MediaScheduling.QueryChannelConcurrency = 64
	cfg.Admission = AdmissionLimits{WorkspaceTasks: 2048, ModelTasks: 2048}
	app, api, _ := admissionCapacityOpen(t, cfg)
	provider := app.media.(*AsyncMediaProvider)
	provider.queryClient.Transport = gateway.transport(provider.queryClient.Transport)
	ctx, cancel := context.WithTimeout(t.Context(), p.Warm+p.Steady+p.DrainLimit+4*time.Minute)
	defer cancel()
	users := openLoopUsers259(t, ctx, api.URL)
	start := time.Now().Add(500 * time.Millisecond)
	report.write(t, map[string]any{"type": "configuration", "profile": p.Name, "fault": fault, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "planned_slots": p.slots(), "slot_ms": p.Slot.Milliseconds(), "service_life_ms": p.Life.Milliseconds(), "query_hint_ms": p.Poll.Milliseconds(), "handler_delay_ms": p.Handler.Milliseconds(), "warmup_ms": p.Warm.Milliseconds(), "steady_ms": p.Steady.Milliseconds(), "window_ms": p.Window.Milliseconds(), "observer_interval_ms": p.Sample.Milliseconds(), "query_workers": 64, "query_pg_connections": 4, "host": "owned loopback HTTP; application, driver, gateway share one process; PostgreSQL separate", "media_outcome": "known supplier failure"})
	runOpenLoop259(t, ctx, p, start, report, app, api.URL, gateway, users, admin, observer)
}
