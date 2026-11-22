package studio

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRunStreamUpgrade020(t *testing.T) {
	database := testDatabase(t)
	conn := deliveryAdmin(t, database)
	ctx := context.Background()
	old := mediaSchedulerSchema019(t, conn)
	name := "migrations/020_media_schedule.sql"
	body, err := fs.ReadFile(migrationFiles, name)
	if err != nil {
		t.Fatal(err)
	}
	old[name] = &fstest.MapFile{Data: body}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO studio_workspaces(id,name) VALUES('ws_demo','legacy');
		INSERT INTO studio_projects(id,workspace_id,name) VALUES('prj_demo','ws_demo','legacy');
		INSERT INTO studio_runs(id,workspace_id,project_id,document) VALUES
		('run_old','ws_demo','prj_demo','{"id":"run_old","workspace_id":"ws_demo","project_id":"prj_demo","status":"RUNNING","output":"old prefix","seq":17}')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err = maintenanceReachable(ctx, conn); err == nil {
		t.Fatal("maintenance accepted an actual 020 installation")
	}
	cfg, _, _ := recoveryFixtureConfig(t, database)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	var seq uint64
	var output, status string
	if err = conn.QueryRow(ctx, `SELECT (document->>'seq')::bigint,document->>'output',document->>'status' FROM studio_runs WHERE id='run_old'`).Scan(&seq, &output, &status); err != nil || seq != 17+recoverySequenceGap || output != "old prefix" || status != "INTERRUPTED" {
		t.Fatal("legacy recovery changed the published prefix or cursor contract", err)
	}
	if outboxCount(t, conn, "SELECT count(*) FROM studio_run_streams") != 0 || outboxCount(t, conn, "SELECT count(*) FROM studio_run_stream_events") != 0 || outboxCount(t, conn, "SELECT max(version) FROM studio_schema_versions") != maintenanceSchemaVersion {
		t.Fatal("migration invented historical stream evidence or omitted the format fence")
	}
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = applyMigrationsFrom(ctx, tx, old); err == nil || !strings.Contains(err.Error(), "newer than this executable") {
		t.Fatal("020 executable accepted a newer format")
	}
}

// The second increment waits on a real row lock. The fault therefore targets
// this append transaction, independent of outbox polling or checkpoint timing.
func TestPostgresRunStreamProcessCommitBoundary(t *testing.T) {
	testStreamProcessCommitBoundary(t, false)
}

func testStreamProcessCommitBoundary(t *testing.T, batch bool) {
	t.Helper()
	for _, mode := range []string{"kill-before-commit", "lost-commit-ack"} {
		t.Run(mode, func(t *testing.T) {
			database := testDatabase(t)
			admin := deliveryAdmin(t, database)
			ctx := context.Background()
			var upstream atomic.Int32
			advance := make(chan struct{})
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				upstream.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				for _, delta := range []string{"first", "second"} {
					if delta == "second" {
						select {
						case <-advance:
						case <-r.Context().Done():
							return
						}
					}
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", delta)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			t.Cleanup(gateway.Close)
			proxy, armed, dropped := recoveryControlAckProxy(t, database)
			cfg := durableProcessConfig{DatabaseURL: proxy, AssetDir: t.TempDir(), ReferenceDir: t.TempDir(), GatewayURL: gateway.URL, ChatGatewayURL: gateway.URL + "/v1", RunDeltaBatching: batch}
			child := startDurableChild(t, cfg)
			client := authClient(t)
			user := registerFixture(t, client, child.base, "stream-crash@example.test")
			key := newID("key")
			input := RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "crash fixture"}
			code, data, _ := authRequest(t, client, child.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
			if code != 201 {
				t.Fatal("chat not accepted", code)
			}
			run := decode[Run](t, data)
			path := "/api/v1/runs/" + run.ID
			streamCtx, cancelStream := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancelStream()
			req, err := http.NewRequestWithContext(streamCtx, "GET", child.base+path+"/events?after=0", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Project-ID", user.Project.ID)
			streamClient := &http.Client{Jar: client.Jar, Transport: client.Transport}
			resp, err := streamClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			kind, cursor, event := readSSEFixture(t, reader)
			if kind != "message.delta" || string(event["delta"]) != `"first"` {
				t.Fatal("first public increment missing", kind)
			}
			firstSeq, err := strconv.ParseUint(cursor, 10, 64)
			if err != nil || outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND seq=$2 AND delta='first'", run.ID, firstSeq) != 1 {
				t.Fatal("browser saw an increment before it committed")
			}
			// Keep the authorized original reader connected while the database
			// outcome is pending. Inspect its remaining bytes after process exit.
			hotSuffix := make(chan []byte, 1)
			go func() {
				body, _ := io.ReadAll(io.LimitReader(reader, 64<<10))
				hotSuffix <- body
			}()
			barrier, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(ctx)
			if _, err = barrier.Exec(ctx, "SELECT run_id FROM studio_run_streams WHERE run_id=$1 FOR UPDATE", run.ID); err != nil {
				t.Fatal(err)
			}
			close(advance)
			var writer uint32
			deadline := time.Now().Add(4 * time.Second)
			for writer == 0 && time.Now().Before(deadline) {
				if err = barrier.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND pg_backend_pid()=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`).Scan(&writer); err != nil {
					t.Fatal(err)
				}
				if writer == 0 {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if writer == 0 {
				t.Fatal("second increment did not reach the locked stream")
			}
			select {
			case <-hotSuffix:
				t.Fatal("original live reader closed before the pending append fault")
			default:
			}
			wantOutput, wantSeq := "first", firstSeq+1
			if mode == "kill-before-commit" {
				child.kill(t)
				if err = barrier.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				armed.Store(writer)
				if err = barrier.Rollback(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case <-dropped:
				case <-time.After(5 * time.Second):
					t.Fatal("targeted append COMMIT response was not lost")
				}
				for deadline := time.Now().Add(5 * time.Second); ; {
					code, _, _ := authRequest(t, client, child.base, "GET", "/readyz", "", "", nil)
					if code == http.StatusServiceUnavailable {
						break
					}
					if code != http.StatusOK || time.Now().After(deadline) {
						t.Fatal("writer did not observe unknown commit before process exit", code)
					}
					time.Sleep(10 * time.Millisecond)
				}
				child.kill(t)
				wantOutput, wantSeq = "firstsecond", firstSeq+2
			}
			select {
			case raw := <-hotSuffix:
				if len(raw) == 64<<10 || bytes.Contains(raw, []byte("second")) || bytes.Contains(raw, []byte("message.delta")) {
					t.Fatal("live reader received unconfirmed output or exceeded the fixture bound")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("original live reader did not exit")
			}
			rows := outboxCount(t, admin, "SELECT count(*) FROM studio_run_stream_events WHERE run_id=$1 AND seq=$2 AND delta='second'", run.ID, firstSeq+1)
			if (rows == 1) != (mode == "lost-commit-ack") || rows > 1 {
				t.Fatal("independent pre-recovery view disagrees with injected fault")
			}
			cfg.DatabaseURL = database
			restarted := startDurableChild(t, cfg)
			code, raw, headers := authRequest(t, client, restarted.base, "POST", "/api/v1/runs", key, user.Project.ID, input)
			got := decode[Run](t, raw)
			if code != 201 || headers.Get("Idempotency-Replayed") != "true" || got.ID != run.ID || got.Output != wantOutput || got.Status != "INTERRUPTED" || got.Seq != wantSeq || upstream.Load() != 1 {
				t.Fatalf("restart or original-key replay lost the committed prefix: status=%s seq=%d calls=%d", got.Status, got.Seq, upstream.Load())
			}
			code, raw, _ = authRequest(t, client, restarted.base, "GET", path+"/events?after="+cursor, "", user.Project.ID, nil)
			if code != 200 || !bytes.Contains(raw, []byte("run.interrupted")) || bytes.Contains(raw, []byte(`"delta":"first"`)) || bytes.Contains(raw, []byte(`"delta":"second"`)) != (mode == "lost-commit-ack") {
				t.Fatal("restarted SSE did not match the committed suffix", code)
			}
			restarted.kill(t)
		})
	}
}

func TestPostgresRunStreamBackupRestore(t *testing.T) {
	bin := os.Getenv("TEST_POSTGRES_BIN_DIR")
	if bin == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR required for a real dump/restore")
	}
	database := testDatabase(t)
	cfg := Config{DatabaseURL: database, AuthMode: "required", ReferenceDir: t.TempDir(), AssetDir: t.TempDir(), MediaDelivery: MediaDeliveryConfig{EncryptionKey: coreDeliveryKey()}, PollInterval: time.Hour}
	app, server := testApp(t, cfg)
	var calls atomic.Int32
	app.chat = chatFunc(func(ctx context.Context, _ string, emit func(string) error) error {
		calls.Add(1)
		for _, text := range []string{"one", "two", "three"} {
			if err := emit(text); err != nil {
				return err
			}
		}
		return nil
	})
	client := authClient(t)
	user := registerFixture(t, client, server.URL, "stream-dump@example.test")
	key := newID("key")
	run := executionRun(t, server, client, user, "studio-chat", "backup fixture", key)
	got := awaitAuthRun(t, client, server.URL, run.ID)
	if got.Status != "COMPLETED" || got.Output != "onetwothree" {
		t.Fatal("original run did not complete")
	}
	path := "/api/v1/runs/" + run.ID + "/events?after=1"
	code, before, _ := authRequest(t, client, server.URL, "GET", path, "", user.Project.ID, nil)
	if code != 200 || !bytes.Contains(before, []byte(`"delta":"two"`)) {
		t.Fatal("original stream tail unavailable")
	}
	app.Close()
	server.Close()
	ctx := context.Background()
	admin := deliveryAdmin(t, database)
	tables := []string{"studio_run_streams", "studio_run_stream_events", "studio_run_executor", "studio_storage_identity"}
	state := schedulerOriginalSnapshot(t, admin, tables)
	u, err := url.Parse(database)
	if err != nil || !strings.HasPrefix(u.Query().Get("search_path"), "studio_test_") {
		t.Fatal("stream restore requires an isolated schema")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "stream.dump")
	deliveryPostgresTool(t, bin, "pg_dump", database, "--no-password", "--no-owner", "--no-acl", "--format=custom", "--schema="+u.Query().Get("search_path"), "--file="+archive)
	deliveryCopyPrivateTree(t, cfg.AssetDir, filepath.Join(dir, "assets"))
	deliveryCopyPrivateTree(t, cfg.ReferenceDir, filepath.Join(dir, "refs"))
	keyPath := filepath.Join(dir, "delivery.key")
	if err = os.WriteFile(keyPath, []byte(cfg.MediaDelivery.EncryptionKey), 0600); err != nil {
		t.Fatal("could not back up the fixture delivery key")
	}
	name := newID("studio_stream_restore")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal("could not create the isolated restore database")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("stream restore fixture cleanup failed")
		}
	})
	u.Path = "/" + name
	cfg.DatabaseURL = u.String()
	deliveryPostgresTool(t, bin, "pg_restore", cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--dbname="+name, archive)
	restoredAdmin := deliveryAdmin(t, cfg.DatabaseURL)
	if !reflect.DeepEqual(state, schedulerOriginalSnapshot(t, restoredAdmin, tables)) {
		t.Fatal("restore changed execution identity, stream rows, cursors or directory binding")
	}
	cfg.AssetDir, cfg.ReferenceDir = t.TempDir(), t.TempDir()
	deliveryCopyPrivateTree(t, filepath.Join(dir, "assets"), cfg.AssetDir)
	deliveryCopyPrivateTree(t, filepath.Join(dir, "refs"), cfg.ReferenceDir)
	secret, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("could not restore the fixture key")
	}
	cfg.MediaDelivery.EncryptionKey = string(secret)
	restored, target := testApp(t, cfg)
	restored.chat = app.chat
	code, after, _ := authRequest(t, client, target.URL, "GET", path, "", user.Project.ID, nil)
	if code != 200 || !bytes.Equal(before, after) {
		t.Fatal("restored authorized cursor produced different SSE data")
	}
	input := RunInput{ProjectID: user.Project.ID, ModelID: "studio-chat", Input: "backup fixture"}
	code, data, headers := authRequest(t, client, target.URL, "POST", "/api/v1/runs", key, user.Project.ID, input)
	if code != 201 || decode[Run](t, data).ID != run.ID || headers.Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
		t.Fatal("restored original key duplicated the run")
	}
}
