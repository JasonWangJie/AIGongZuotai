//go:build releasedrill

package studio

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/jackc/pgx/v5"
)

// This phase process must never migrate a database or create owner markers.
// The supplied DSN is accepted only for the corresponding fresh PITR fixture.
func TestReleaseProcessOwnership(t *testing.T) {
	phase := os.Getenv("RELEASE_CHECK_PHASE")
	if phase == "" {
		t.Skip("invoked by the isolated release drill")
	}
	drillID, root := os.Getenv("RELEASE_DRILL_ID"), os.Getenv("RELEASE_FIXTURE")
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || u.Scheme != "postgresql" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/studio" || u.RawQuery != "sslmode=disable" || !regexp.MustCompile(`^pitr_[a-f0-9]{32}$`).MatchString(drillID) {
		t.Fatal("unrecognized private fixture connection")
	}
	if u.User == nil || !regexp.MustCompile("^drill_[a-f0-9]{16}$").MatchString(u.User.Username()) {
		t.Fatal("unrecognized private fixture role")
	}
	cluster := os.Getenv("RELEASE_CHECK_CLUSTER")
	if cluster == "" {
		cluster = "source"
	}
	if cluster != "source" && cluster != "restored" {
		t.Fatal("unsupported release fixture cluster")
	}
	format := maintenanceSchemaVersion
	if value := os.Getenv("RELEASE_CHECK_FORMAT"); value != "" {
		if value != "36" && value != "37" {
			t.Fatal("unsupported cross-format fixture")
		}
		format, _ = strconv.Atoi(value)
	}
	source := filepath.Join(root, cluster)
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "studio-pitr-") || !strings.EqualFold(source, resolved) {
		t.Fatal("unrecognized private fixture directory")
	}
	// Match the private postmaster record before opening a database socket.
	pid, err := os.ReadFile(filepath.Join(source, "postmaster.pid"))
	fields := strings.Split(strings.ReplaceAll(string(pid), "\r\n", "\n"), "\n")
	if err != nil || len(fields) < 4 || fields[0] == "" || !strings.EqualFold(filepath.Clean(fields[1]), source) || fields[3] != u.Port() {
		t.Fatal("local source directory and port do not match")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("private fixture unreachable")
	}
	defer conn.Close(ctx)
	var marker, dataDir, database string
	if err = conn.QueryRow(ctx, "SELECT (SELECT id FROM public.pitr_drill_identity),current_setting('data_directory'),current_database()").Scan(&marker, &dataDir, &database); err != nil || marker != drillID || database != "studio" || !strings.EqualFold(filepath.Clean(dataDir), source) {
		t.Fatal("private fixture identity differs")
	}
	result := map[string]any{"drillId": drillID, "schemaVersion": format}
	if phase == "fresh" {
		var fresh bool
		if err = conn.QueryRow(ctx, "SELECT NOT EXISTS(SELECT FROM pg_class WHERE relnamespace='public'::regnamespace AND relname LIKE 'studio\\_%')").Scan(&fresh); err != nil || !fresh {
			t.Fatal("first start requires an unused workbench database")
		}
	} else if phase == "free" || phase == "held" {
		refPath, assetPath := os.Getenv("REFERENCE_DIR"), os.Getenv("ASSET_DIR")
		if !strings.EqualFold(filepath.Dir(filepath.Dir(refPath)), root) || filepath.Dir(refPath) != filepath.Dir(assetPath) || filepath.Base(refPath) != "references" || filepath.Base(assetPath) != "assets" {
			t.Fatal("private directory scope differs")
		}
		var locksFree bool
		if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(0x53545544494F)).Scan(&locksFree); err != nil || locksFree != (phase == "free") {
			t.Fatal("unexpected PostgreSQL writer lock state")
		}
		// Keep all successful locks until ownership and format checks complete.
		for i, dir := range []string{refPath, assetPath} {
			lockPath := filepath.Join(dir, storageLeaseName)
			info, err := os.Lstat(lockPath)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatal("missing original regular OS lock file")
			}
			lock := flock.New(lockPath)
			defer lock.Close()
			got, err := lock.TryLock()
			if err != nil || got != locksFree {
				t.Fatal("unexpected private directory lock state", i)
			}
			result[[]string{"referenceOS", "assetOS"}[i]] = got
		}
		ref, err := os.OpenRoot(refPath)
		if err != nil {
			t.Fatal("references unavailable")
		}
		defer ref.Close()
		asset, err := os.OpenRoot(assetPath)
		if err != nil {
			t.Fatal("assets unavailable")
		}
		defer asset.Close()
		if err = requireStorageOwnership(ctx, conn, ref, asset); err != nil {
			t.Fatal(err)
		}
		id, err := storageOwnerID(ctx, conn)
		if err != nil || os.Getenv("RELEASE_STORAGE_ID") != "" && os.Getenv("RELEASE_STORAGE_ID") != id {
			t.Fatal("stable storage identity differs")
		}
		var exact bool
		if err = conn.QueryRow(ctx, "SELECT count(*)=$1 AND min(version)=1 AND max(version)=$1 FROM studio_schema_versions", format).Scan(&exact); err != nil || !exact {
			t.Fatal("workbench schema format differs")
		}
		result["databaseID"], result["postgresAdvisory"] = id, locksFree
		if os.Getenv("RELEASE_CHECK_FORMAT") != "" {
			var canvasTables bool
			err = conn.QueryRow(ctx, `SELECT
				(to_regclass('public.studio_canvas_versions') IS NOT NULL)=$1 AND
				(to_regclass('public.studio_canvas_sources') IS NOT NULL)=$1`, format == 37).Scan(&canvasTables)
			if err != nil || !canvasTables {
				t.Fatal("physical canvas relations disagree with the format marker")
			}
			var versions, runs, tasks, reservations, journals int
			err = conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM studio_workflow_versions),
				(SELECT count(*) FROM studio_runs), (SELECT count(*) FROM studio_tasks),
				(SELECT count(*) FROM studio_ledger_reservations), (SELECT count(*) FROM studio_ledger_journals)`).Scan(&versions, &runs, &tasks, &reservations, &journals)
			if err != nil {
				t.Fatal("could not count cross-format fixture records")
			}
			result["business"] = map[string]int{"versions": versions, "runs": runs, "tasks": tasks, "reservations": reservations, "journals": journals}
		}
	} else {
		t.Fatal("unsupported phase")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal("cannot encode phase result")
	}
	t.Log("RELEASE_LOCK_FACTS", string(encoded))
}
