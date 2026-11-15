package studio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mediaScheduleBackupState(t *testing.T, conn *pgx.Conn) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, table := range []string{"studio_task_schedule", "studio_workspace_schedule", "studio_lane_schedule", "studio_media_channel_runtime"} {
		var raw string
		if err := conn.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&raw); err != nil {
			t.Fatal("private scheduling backup state unavailable", err)
		}
		state[table] = raw
	}
	return state
}

// Actual dump/restore preserves a real 429 deadline. No test clock helper may
// shorten it; this case checks continued refusal, while the restart integration
// case separately verifies expiration and a successful original-handle probe.
func TestPostgresMediaSchedulerCooldownBackupRestore(t *testing.T) {
	bin := os.Getenv("TEST_POSTGRES_BIN_DIR")
	if bin == "" {
		t.Skip("TEST_POSTGRES_BIN_DIR absent; real scheduler dump/restore not run")
	}
	fixture := newDeliveryHTTPFixture(t, -1)
	fixture.server.Close()
	var posts, queries atomic.Int32
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureMediaKey {
			w.WriteHeader(401)
			return
		}
		if r.Method == "POST" {
			posts.Add(1)
			w.WriteHeader(202)
			_, _ = w.Write(mediaFixture(t, "accepted-image.json"))
			return
		}
		queries.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}))
	cfg := deliveryTestConfig(t, fixture, true)
	a, server := testApp(t, cfg)
	client, identity := deliveryIdentity(t, server, true)
	task, input, key := laneAccept(t, a, server, client, identity, "private scheduler restore input")
	deliveryStep(t, a, task.ID)
	deliveryStep(t, a, task.ID)
	if posts.Load() != 1 || queries.Load() != 1 {
		t.Fatal("fixture did not reach an actual single 429 query")
	}
	admin := deliveryAdmin(t, cfg.DatabaseURL)
	beforeTask := deliveryRead(t, a, task.ID)
	projection, err := loadMediaScheduleSQL(context.Background(), admin, task.ID)
	if err != nil || projection == nil || projection.ServerNotBefore.Before(time.Now().Add(time.Minute)) {
		t.Fatal("server deadline was not durably recorded", err)
	}
	channel, err := loadMediaChannelSQL(context.Background(), admin, "gateway", "query", projection.ChannelKey, false)
	if err != nil || !channel.ProbeRequired || channel.CooldownUntil.Before(projection.ServerNotBefore) {
		t.Fatal("429 did not preserve channel wait and single-probe requirement", err)
	}
	// Advance actual round-robin selection while it can only skip this task.
	if selected, err := a.selectMediaScheduleCandidate(context.Background(), admin, mediaQueryLane); err != nil || selected != nil {
		t.Fatal("cold task was selected before backup", err)
	}
	a.Close()
	server.Close()
	before := mediaScheduleBackupState(t, admin)
	var originalHandle, originalSubmission string
	if err = admin.QueryRow(context.Background(), `SELECT handle::text,submission::text FROM studio_tasks WHERE id=$1`, task.ID).Scan(&originalHandle, &originalSubmission); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.DatabaseURL)
	if err != nil || !strings.HasPrefix(u.Query().Get("search_path"), "studio_test_") {
		t.Fatal("scheduler restore requires its isolated schema")
	}
	backup := t.TempDir()
	archive := filepath.Join(backup, "scheduler.dump")
	deliveryPostgresTool(t, bin, "pg_dump", cfg.DatabaseURL, "--no-password", "--no-owner", "--no-acl", "--format=custom", "--schema="+u.Query().Get("search_path"), "--file="+archive)
	deliveryCopyPrivateTree(t, cfg.AssetDir, filepath.Join(backup, "assets"))
	deliveryCopyPrivateTree(t, cfg.ReferenceDir, filepath.Join(backup, "references"))
	if err = os.WriteFile(filepath.Join(backup, "delivery.key"), []byte(cfg.MediaDelivery.EncryptionKey), 0600); err != nil {
		t.Fatal("private key backup failed")
	}
	database := newID("studio_schedule_restore")
	if _, err = admin.Exec(context.Background(), "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal("isolated restore needs CREATE DATABASE privilege")
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
			t.Error("isolated scheduler restore database cleanup failed")
		}
	})
	u.Path = "/" + database
	restoredURL := u.String()
	deliveryPostgresTool(t, bin, "pg_restore", restoredURL, "--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--dbname="+database, archive)
	restoredAdmin := deliveryAdmin(t, restoredURL)
	if !reflect.DeepEqual(before, mediaScheduleBackupState(t, restoredAdmin)) {
		t.Fatal("dump/restore changed a scheduling projection, cursor, cooldown or probe")
	}
	restoredAssets, restoredRefs := t.TempDir(), t.TempDir()
	deliveryCopyPrivateTree(t, filepath.Join(backup, "assets"), restoredAssets)
	deliveryCopyPrivateTree(t, filepath.Join(backup, "references"), restoredRefs)
	keyBytes, err := os.ReadFile(filepath.Join(backup, "delivery.key"))
	if err != nil {
		t.Fatal("private key restore failed")
	}
	cfg.DatabaseURL, cfg.AssetDir, cfg.ReferenceDir = restoredURL, restoredAssets, restoredRefs
	cfg.MediaDelivery.EncryptionKey = string(keyBytes)
	cfg.mediaProvider = fixture.provider(t, restoredAssets)
	restored, restoredServer := testApp(t, cfg)
	if got := deliveryRead(t, restored, task.ID); got.Status != beforeTask.Status || got.Version != beforeTask.Version {
		t.Fatal("startup changed an accepted task awaiting its original deadline")
	}
	after, err := loadMediaScheduleSQL(context.Background(), restoredAdmin, task.ID)
	if err != nil || after == nil || !after.ServerNotBefore.Equal(projection.ServerNotBefore) || !after.EligibleAt.Equal(projection.EligibleAt) {
		t.Fatal("startup shortened the server or task deadline", err)
	}
	restoredChannel, err := loadMediaChannelSQL(context.Background(), restoredAdmin, "gateway", "query", projection.ChannelKey, false)
	if err != nil || !restoredChannel.CooldownUntil.Equal(channel.CooldownUntil) || restoredChannel.ProbeRequired != channel.ProbeRequired || restoredChannel.ProbeID != channel.ProbeID || restoredChannel.ProbeUntil != nil || channel.ProbeUntil != nil || restoredChannel.EffectiveLimit != channel.EffectiveLimit || restoredChannel.FailureCount != channel.FailureCount || restoredChannel.SuccessCount != channel.SuccessCount || restoredChannel.Version != channel.Version || restoredChannel.Parked != channel.Parked {
		t.Fatal("startup discarded shared channel recovery state even though this task still had a server deadline", err)
	}
	var handle, submission string
	if err = restoredAdmin.QueryRow(context.Background(), `SELECT handle::text,submission::text FROM studio_tasks WHERE id=$1`, task.ID).Scan(&handle, &submission); err != nil || handle != originalHandle || submission != originalSubmission {
		t.Fatal("restore reconstructed a frozen handle or request")
	}
	for range 5 {
		if restored.executePersistentTaskLane(mediaQueryLane, task.ID) || restored.executePersistentTaskLane(mediaQueryLane, "") || restored.executePersistentTaskLane(mediaSubmitLane, task.ID) {
			t.Fatal("restored strict selection bypassed a persisted deadline")
		}
	}
	restoredClient := authClient(t)
	if code, _, _ := authRequest(t, restoredClient, restoredServer.URL, "POST", "/api/v1/auth/login", "", "", map[string]string{"email": "delivery-integration@example.test", "password": "fixture-password-123"}); code != 200 {
		t.Fatal("restored original account could not log in", code)
	}
	code, raw, headers := authRequest(t, restoredClient, restoredServer.URL, "POST", "/api/v1/tasks", key, identity.Project.ID, input)
	if code != 202 || headers.Get("Idempotency-Replayed") != "true" || decode[Task](t, raw).ID != task.ID || posts.Load() != 1 || queries.Load() != 1 {
		t.Fatal("original idempotency after restore generated or queried again", code)
	}
}
