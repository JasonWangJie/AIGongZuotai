package studio

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Observe the database independently of both HTTP readers. Only the counters
// and elapsed lease movement are exported, never the lease or resource IDs.
type longReplyLeaseSamples struct {
	owner, generation string
	epoch, head       int64
	first, last       time.Time
	samples, advances int
}

func (l *longReplyLeaseSamples) observe(ctx context.Context, db *pgx.Conn) error {
	var ready bool
	if err := db.QueryRow(ctx, "SELECT to_regclass('studio_run_executor') IS NOT NULL").Scan(&ready); err != nil || !ready {
		return err
	}
	var owner, generation string
	var epoch, head int64
	var until time.Time
	err := db.QueryRow(ctx, `SELECT lease_owner,lease_generation,lease_epoch,head_seq,lease_until FROM studio_run_streams
		WHERE lease_required AND NOT closed AND lease_owner<>'' AND lease_until>clock_timestamp()`).Scan(&owner, &generation, &epoch, &head, &until)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if l.samples == 0 {
		l.owner, l.generation, l.epoch, l.first = owner, generation, epoch, until
	} else if owner != l.owner || generation != l.generation || epoch != l.epoch || head < l.head || until.Before(l.last) {
		return fmt.Errorf("leased stream changed identity or moved backwards")
	} else if until.After(l.last) {
		l.advances++
	}
	l.head, l.last = head, until
	l.samples++
	return nil
}

func TestPostgresLongReplyBrowserAndReopen(t *testing.T) {
	profile := os.Getenv("LONG_REPLY_BROWSER_TEST")
	if profile == "" {
		t.Skip("requires an opt-in local browser and a built workbench")
	}
	if (profile != "quick" && profile != "full") || !libraryLoadLocalDB(os.Getenv("TEST_DATABASE_URL")) {
		t.Fatal("expected quick or full and a local disposable PostgreSQL database")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	server := os.Getenv("LONG_REPLY_TEST_SERVER")
	dist := os.Getenv("LONG_REPLY_TEST_DIST")
	if !filepath.IsAbs(server) || !filepath.IsAbs(dist) {
		t.Fatal("expected absolute paths for the built server and workbench")
	}
	dsn := testDatabase(t)
	db := deliveryAdmin(t, dsn)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DatabaseURL: dsn, AuthMode: "required", AssetDir: t.TempDir(), ReferenceDir: t.TempDir(),
		MediaDelivery: MediaDeliveryConfig{EncryptionKey: base64.StdEncoding.EncodeToString(key)}, WebDir: dist}
	password := newID("fixture")
	tmpReport := filepath.Join(t.TempDir(), "observations.json")
	args := []string{filepath.Join(root, "scripts", "perf", "long-reply.mjs"), "--server", server, "--dist", dist, "--out", tmpReport}
	samples := 1801
	tick := 15 * time.Second
	deadline := 34 * time.Minute
	if profile == "quick" {
		args = append(args, "--quick")
		samples = 25
		tick = time.Second
		deadline = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "LONG_REPLY_TEST_CONTROL_PIPE=1", "LONG_REPLY_TEST_DATABASE_URL="+dsn, "LONG_REPLY_TEST_ASSET_DIR="+cfg.AssetDir,
		"LONG_REPLY_TEST_REFERENCE_DIR="+cfg.ReferenceDir, "LONG_REPLY_TEST_PASSWORD="+password, "LONG_REPLY_TEST_DELIVERY_KEY="+cfg.MediaDelivery.EncryptionKey)
	control, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal("cannot create benchmark control pipe")
	}
	defer control.Close()
	cmd.Cancel = control.Close
	cmd.WaitDelay = 15 * time.Second
	attach, release, err := longReplyProcessGuard(cmd)
	if err != nil {
		t.Fatal("could not prepare owned benchmark process tree", err)
	}
	defer func() {
		if err := release(); err != nil {
			t.Error("could not release benchmark process tree", err)
		}
	}()
	// The driver reports fixed phases and counters. Private inputs are never
	// included in a test failure, even if a child unexpectedly logs them.
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal("could not start the browser driver")
	}
	if err := attach(); err != nil {
		control.Close()
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("could not attach the isolated benchmark driver", err)
	}
	if _, err := fmt.Fprintln(control, "start"); err != nil {
		control.Close()
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("could not start the owned benchmark")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTicker(tick)
	defer timer.Stop()
	var lease longReplyLeaseSamples
	var sampleErr error
running:
	for {
		select {
		case err := <-done:
			if err != nil {
				// Only pass through the driver's short, fixed-format phase codes.
				for _, line := range strings.Split(diagnostic.String(), "\n") {
					if longReplyDiagnostic.MatchString(strings.TrimSpace(line)) {
						t.Log(strings.TrimSpace(line))
					}
				}
				t.Fatal("long reply driver failed", err)
			}
			break running
		case <-timer.C:
			if sampleErr == nil {
				probe, stop := context.WithTimeout(ctx, 3*time.Second)
				sampleErr = lease.observe(probe, db)
				stop()
				if profile == "full" && sampleErr == nil && lease.samples > 0 && lease.samples%20 == 0 {
					t.Logf("database lease samples=%d current committed sequence=%d", lease.samples, lease.head)
				}
			}
		}
	}
	// Empty the owned process tree before reopening the same private state.
	if err := release(); err != nil {
		t.Fatal("benchmark descendants remained after driver exit", err)
	}
	if sampleErr != nil || lease.samples == 0 || (profile == "full" && (lease.advances < 2 || lease.last.Sub(lease.first) < 28*time.Minute)) {
		t.Fatalf("insufficient stable lease evidence: samples=%d advances=%d", lease.samples, lease.advances)
	}
	var original Run
	var doc []byte
	var count, nodes, events, head, floor, snapshot, outputBytes int
	var closed, released bool
	err = db.QueryRow(ctx, `SELECT r.document,h.head_seq,h.floor_seq,h.snapshot_seq,h.output_bytes,h.closed,
		h.lease_required AND h.lease_owner='' AND h.lease_generation='' AND h.lease_until IS NULL,
		(SELECT count(*) FROM studio_runs),(SELECT count(*) FROM studio_chat_messages),
		(SELECT count(*) FROM studio_run_stream_events e WHERE e.run_id=r.id)
		FROM studio_runs r JOIN studio_run_streams h ON h.run_id=r.id`).Scan(&doc, &head, &floor, &snapshot, &outputBytes, &closed, &released, &count, &nodes, &events)
	if err != nil || json.Unmarshal(doc, &original) != nil {
		t.Fatal("cannot read committed terminal stream")
	}
	var expected strings.Builder
	for i := 0; i < samples; i++ {
		fmt.Fprintf(&expected, "段%04d\n.%04d\n", i, i)
	}
	if original.Status != "COMPLETED" || original.Output != expected.String() || count != 1 || nodes != 2 || !closed || !released ||
		head != samples+1 || snapshot != head || int(original.Seq) != head || outputBytes != expected.Len() || events < 1 || events > runStreamRetention ||
		(profile == "full" && floor == 0) {
		t.Fatal("terminal run, bounded replay window, lease or committed text mismatch")
	}
	app, web := testApp(t, cfg)
	reader := authClient(t)
	code, _, _ := authRequest(t, reader, web.URL, http.MethodPost, "/api/v1/auth/login", "", "", map[string]string{"email": "long-stream@example.test", "password": password})
	if code != 200 {
		t.Fatal("reopened account login failed", code)
	}
	code, raw, _ := authRequest(t, reader, web.URL, http.MethodGet, "/api/v1/runs/"+original.ID+"/snapshot", "", "", nil)
	var recovered Run
	if code != 200 || json.Unmarshal(raw, &recovered) != nil || recovered.Output != original.Output || recovered.Status != original.Status || recovered.Seq != original.Seq {
		t.Fatal("original stream did not survive application restart")
	}
	web.Close()
	app.Close()
	raw, err = os.ReadFile(tmpReport)
	if err != nil {
		t.Fatal("missing child observations")
	}
	var report map[string]any
	if json.Unmarshal(raw, &report) != nil {
		t.Fatal("invalid child observations")
	}
	sum := sha256.Sum256([]byte(original.Output))
	if report["storage"] != "postgres" || report["output_sha256"] != hex.EncodeToString(sum[:]) || report["segments"] != float64(samples) || report["final_sequence"] != float64(head) {
		t.Fatal("database and browser report identities disagree")
	}
	report["postgres"] = map[string]any{"run_count": count, "message_nodes": nodes, "head_sequence": head, "floor_sequence": floor,
		"retained_events": events, "output_bytes": outputBytes, "lease_observations": lease.samples, "lease_deadline_advances_observed": lease.advances,
		"lease_advance_span_ms": lease.last.Sub(lease.first).Milliseconds(), "lease_released": released, "reopened_original_snapshot": true}
	if target := os.Getenv("LONG_REPLY_PG_REPORT"); target != "" {
		if !filepath.IsAbs(target) {
			t.Fatal("report destination must be absolute")
		}
		body, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("cannot create new report")
		}
		_, writeErr := file.Write(append(body, '\n'))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("could not save report")
		}
	}
	t.Logf("completed %d deltas, %d retained events, %d lease observations, verified original snapshot after reopen", samples, events, lease.samples)
}

var longReplyDiagnostic = regexp.MustCompile(`^(?:long-reply (?:provider-requested|service-ready|upstream-segment-[0-9]{1,4})|Long reply benchmark failed\. [A-Za-z0-9 :;.,=()-]{1,140})$`)
