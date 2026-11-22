package streamjournalpoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fixture struct {
	journal
	root     *pgx.Conn
	cfg      *pgx.ConnConfig
	settings map[string]string
}

var errLocalDatabase = errors.New("journal PoC requires a loopback TCP test database")

func journalLocalIPs(host string) ([]string, error) {
	if strings.EqualFold(host, "localhost") {
		// Do not resolve localhost through DNS or the hosts file.
		return []string{"127.0.0.1", "::1"}, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
		return nil, errLocalDatabase
	}
	return []string{ip.Unmap().String()}, nil
}

func restrictJournalDial(dial pgconn.DialFunc) pgconn.DialFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		// Check the literal destination again, including a later pgx cancel
		// connection. No name resolver or service-name lookup runs here.
		endpoint, err := netip.ParseAddrPort(address)
		if err != nil || endpoint.Port() == 0 || endpoint.Addr().Zone() != "" ||
			!endpoint.Addr().Unmap().IsLoopback() || (network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, errLocalDatabase
		}
		return dial(ctx, network, net.JoinHostPort(endpoint.Addr().Unmap().String(), strconv.Itoa(int(endpoint.Port()))))
	}
}

func journalDatabaseConfig(dsn string) (*pgx.ConnConfig, error) {
	// A service may pull hosts from another file. Explicit credentials, TLS
	// mode and normal local connection parameters suffice for this fixture.
	if dsn == "" || os.Getenv("PGSERVICE") != "" || os.Getenv("PGSERVICEFILE") != "" {
		return nil, errLocalDatabase
	}
	cfg, err := pgx.ParseConfigWithOptions(dsn, pgx.ParseConfigOptions{ParseConfigOptions: pgconn.ParseConfigOptions{
		ConnStringAllowedKeys: []string{"host", "port", "database", "user", "password", "sslmode", "connect_timeout", "application_name", "search_path", "target_session_attrs"},
	}})
	if err != nil {
		return nil, errLocalDatabase
	}
	if _, err = journalLocalIPs(cfg.Host); err != nil {
		return nil, err
	}
	for _, fb := range cfg.Fallbacks {
		if _, err = journalLocalIPs(fb.Host); err != nil {
			return nil, err
		}
	}
	cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return journalLocalIPs(host) }
	cfg.DialFunc = restrictJournalDial(cfg.DialFunc)
	return cfg, nil
}

type schemaExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// Generate the name only from this fixture's random bytes. Return the cleanup
// capability only after PostgreSQL positively acknowledges CREATE.
func createJournalSchema(ctx context.Context, c schemaExecutor, nonce [12]byte) (string, func(context.Context) error, error) {
	name := pgx.Identifier{"journal_poc_" + hex.EncodeToString(nonce[:])}.Sanitize()
	if _, err := c.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		return "", nil, err
	}
	return name, func(ctx context.Context) error {
		_, err := c.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
		return err
	}, nil
}

func connect(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error) {
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return nil, errors.New("cannot open isolated PostgreSQL connection")
	}
	return c, nil
}
func isolate(t *testing.T, g layout) *fixture {
	t.Helper()
	profile := os.Getenv("STREAM_JOURNAL_PROFILE")
	if profile == "" {
		t.Skip("set STREAM_JOURNAL_PROFILE=pilot or full to opt in")
	}
	if profile != "pilot" && profile != "full" {
		t.Fatal("invalid STREAM_JOURNAL_PROFILE")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL required for opt-in PoC")
	}
	cfg, e := journalDatabaseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, e := connect(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	var rnd [12]byte
	if _, e = rand.Read(rnd[:]); e != nil {
		t.Fatal(e)
	}
	dataset := hex.EncodeToString(rnd[:])
	name, drop, e := createJournalSchema(ctx, c, rnd)
	if e != nil {
		t.Fatal("unconfirmed schema creation; no schema cleanup registered")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if e := drop(ctx); e != nil {
			t.Error("failed to clean own temporary schema")
		}
	})
	if _, e = c.Exec(ctx, "SET search_path TO "+name+", pg_catalog"); e != nil {
		t.Fatal(e)
	}
	cfg = cfg.Copy()
	cfg.RuntimeParams["search_path"] = name + ",pg_catalog"
	f := &fixture{journal: journal{g, dataset}, root: c, cfg: cfg, settings: map[string]string{}}
	// Require durable local WAL at commit; do not change any server setting.
	for _, setting := range []string{"fsync", "full_page_writes", "synchronous_commit", "wal_level", "server_version", "max_connections", "shared_buffers", "wal_compression"} {
		var value string
		if e = c.QueryRow(ctx, "SHOW "+setting).Scan(&value); e != nil {
			t.Fatal(e)
		}
		f.settings[setting] = value
	}
	if f.settings["fsync"] != "on" || f.settings["full_page_writes"] != "on" || (f.settings["synchronous_commit"] != "on" && f.settings["synchronous_commit"] != "remote_apply") {
		t.Fatal("PoC requires fsync=on, full_page_writes=on, synchronous_commit=on/remote_apply")
	}
	for _, sql := range []string{
		"CREATE TABLE shards (id integer PRIMARY KEY,pos bigint NOT NULL DEFAULT 0 CHECK(pos>=0))",
		"CREATE TABLE heads (id integer PRIMARY KEY,shard integer NOT NULL REFERENCES shards(id),epoch integer NOT NULL DEFAULT 1 CHECK(epoch>0),seq integer NOT NULL DEFAULT 0 CHECK(seq>=0)) WITH (fillfactor=75)",
		fmt.Sprintf("CREATE TABLE batches (shard integer REFERENCES shards(id),pos bigint NOT NULL CHECK(pos>0),n integer NOT NULL CHECK(n BETWEEN 1 AND %d),body bytea NOT NULL CHECK(octet_length(body)=n*%d),PRIMARY KEY(shard,pos))", maxBatch, recordBytes),
	} {
		if _, e = c.Exec(ctx, sql); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = c.Exec(ctx, "INSERT INTO shards(id) SELECT generate_series(0,$1::int-1)", g.shards); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Exec(ctx, "INSERT INTO heads(id,shard) SELECT i,(i-1)%$2::int FROM generate_series(1,$1::int)i", g.runs, g.shards); e != nil {
		t.Fatal(e)
	}
	var logged int
	if e = c.QueryRow(ctx, "SELECT count(*) FROM pg_class WHERE relnamespace=current_schema()::regnamespace AND relname IN ('batches','heads','shards') AND relpersistence='p'").Scan(&logged); e != nil || logged != 3 {
		t.Fatal("journal tables are not regular WAL-logged relations")
	}
	f.settings["regular_wal_logged_relations"] = "3"
	return f
}
func (f *fixture) conn(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, e := connect(ctx, f.cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	var syncMode, fsync string
	if e := c.QueryRow(ctx, "SELECT current_setting('synchronous_commit'),current_setting('fsync')").Scan(&syncMode, &fsync); e != nil || fsync != "on" || (syncMode != "on" && syncMode != "remote_apply") {
		t.Fatal("connection is not using durable local WAL commits")
	}
	return c
}
func TestPostgresStreamJournalCommitAndEpoch(t *testing.T) {
	f := isolate(t, layout{4, 2})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	writer, other := f.conn(t), f.conn(t)
	batch := []delta{newDelta(1, 1, 1, 1), newDelta(3, 1, 1, 2)}
	tx, e := writer.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	// Roll back even on a failed assertion before dropping the test schema.
	defer tx.Rollback(context.Background())
	if pos, e := f.stage(ctx, tx, 0, batch); e != nil || pos != 1 {
		t.Fatalf("stage %d %v", pos, e)
	}
	var count int
	if e = other.QueryRow(ctx, "SELECT count(*) FROM batches").Scan(&count); e != nil || count != 0 {
		t.Fatal("in-flight batch visible")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if pos, e := f.append(ctx, writer, 0, batch); e != nil || pos != 1 {
		t.Fatalf("rollback left a sequence or position %d %v", pos, e)
	}
	if e = f.rotate(ctx, other, 1, 1); e != nil {
		t.Fatal(e)
	}
	bad := []delta{newDelta(3, 1, 2, 3), newDelta(1, 1, 2, 4)}
	if _, e = f.append(ctx, writer, 0, bad); !errors.Is(e, errFence) {
		t.Fatal("stale epoch accepted")
	}
	// Competing transactions have the same prior sequence; exactly one may win.
	good := []delta{newDelta(1, 2, 2, 3), newDelta(3, 1, 2, 4)}
	result := make(chan error, 2)
	start := make(chan struct{})
	for _, c := range []*pgx.Conn{writer, other} {
		go func() { <-start; _, e := f.append(ctx, c, 0, good); result <- e }()
	}
	close(start)
	a, b := <-result, <-result
	if !(a == nil && errors.Is(b, errFence) || b == nil && errors.Is(a, errFence)) {
		t.Fatalf("concurrent append results %v %v", a, b)
	}
	cur := cursor{f.dataset, 0, 1, 0}
	var visible []delta
	out, e := f.read(ctx, other, cur, 0, 1, 2, func(d delta) error { visible = append(visible, d); return nil })
	if e != nil || out.after != 2 || len(visible) != 2 || visible[0] != batch[0] || visible[1] != good[0] {
		t.Fatal("committed replay or epoch mismatch")
	}
	for _, wrong := range []cursor{{f.dataset, 0, 3, 0}, {"other", 0, 1, 0}, {f.dataset, 1, 1, 0}, {f.dataset, 0, 1, 3}} {
		if _, e = f.read(ctx, other, wrong, 0, 1, 2, func(delta) error { return nil }); e == nil {
			t.Fatal("cursor accepted outside its run, shard, dataset or committed head")
		}
	}
	for s := uint32(3); s <= 8; s++ {
		if _, e = f.append(ctx, writer, 0, []delta{newDelta(1, 2, s, 5)}); e != nil {
			t.Fatal(e)
		}
	}
	var seq uint32 = 2
	for out.after < 8 {
		old := out.after
		out, e = f.read(ctx, other, out, 0, 1, 8, func(d delta) error {
			seq++
			if d.seq != seq {
				return errFence
			}
			return nil
		})
		if e != nil || out.after <= old || out.after-old > maxPage {
			t.Fatal("invalid bounded replay", e)
		}
	}
	if seq != 8 {
		t.Fatal("missing paginated output")
	}
	original := []delta{newDelta(1, 2, 9, 6)}
	if _, e = f.append(ctx, writer, 0, original); e != nil {
		t.Fatal(e)
	}
	// The durable transaction is real. Withhold its application notification
	// and retain the last acknowledged position without appending again.
	if _, e = f.read(ctx, other, out, 0, 1, 8, func(delta) error { t.Fatal("unconfirmed position was presented"); return nil }); e != nil {
		t.Fatal(e)
	}
	if ok, e := f.confirmed(ctx, other, 0, 9, []delta{newDelta(1, 2, 9, 7)}); e != nil || ok {
		t.Fatal("confirmed different original bytes")
	}
	if ok, e := f.confirmed(ctx, other, 0, 9, original); e != nil || !ok {
		t.Fatal("missing exact original commit")
	}
	out, e = f.read(ctx, other, out, 0, 1, 9, func(d delta) error {
		if d != original[0] {
			return errFence
		}
		return nil
	})
	if e != nil || out.after != 9 {
		t.Fatal("original ACK reconciliation failed")
	}
}
