package streamjournalpoc

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestJournalDatabaseLoopback(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGHOST", "192.0.2.7")
	const common = " user=fixture password=fixture dbname=fixture sslmode=disable"
	for _, dsn := range []string{
		"host=192.0.2.7" + common,
		"host=localhost,192.0.2.7" + common,
		"host=192.0.2.7,127.0.0.1" + common,
		"host=/tmp" + common,
		common, // Host inherited from the environment.
		"postgres://fixture:fixture@localhost/fixture?host=192.0.2.7&sslmode=disable",
		"postgres://fixture:fixture@localhost/fixture?service=fixture",
		"postgres://fixture:fixture@localhost/fixture?hostaddr=192.0.2.7",
	} {
		if cfg, err := journalDatabaseConfig(dsn); cfg != nil || !errors.Is(err, errLocalDatabase) {
			t.Fatal("nonlocal or indirect destination accepted")
		}
	}
	cfg, err := journalDatabaseConfig("host=localhost,::1,127.0.0.1" + common)
	if err != nil || len(cfg.Fallbacks) != 2 {
		t.Fatal("explicit local host list rejected")
	}
	ctx := context.Background()
	for host, want := range map[string][]string{
		"LocalHost": {"127.0.0.1", "::1"}, "::1": {"::1"}, "127.0.0.1": {"127.0.0.1"},
	} {
		got, err := cfg.LookupFunc(ctx, host)
		if err != nil || !slices.Equal(got, want) {
			t.Fatal("local hostname was not pinned to literal loopback")
		}
	}
	if _, err := cfg.LookupFunc(ctx, "elsewhere.invalid"); !errors.Is(err, errLocalDatabase) {
		t.Fatal("lookup accepted a nonlocal name")
	}
	t.Setenv("PGSERVICE", "inherited")
	if _, err := journalDatabaseConfig("host=localhost" + common); !errors.Is(err, errLocalDatabase) {
		t.Fatal("ambient service file configuration accepted")
	}
}

func TestJournalDialEndpoint(t *testing.T) {
	var calls []string
	// No real socket: count only calls that would have reached the OS dialer.
	dial := restrictJournalDial(func(_ context.Context, network, address string) (net.Conn, error) {
		calls = append(calls, network+" "+address)
		return nil, nil
	})
	ctx := context.Background()
	for _, pair := range [][2]string{
		{"tcp", "192.0.2.7:5432"}, {"tcp", "[2001:db8::1]:5432"}, {"tcp", "localhost:5432"},
		{"tcp", "127.0.0.1:postgres"}, {"tcp", "127.0.0.1:0"}, {"udp", "127.0.0.1:5432"},
		{"unix", "/tmp/.s.PGSQL.5432"}, {"tcp", "[::1%scope]:5432"},
	} {
		if _, err := dial(ctx, pair[0], pair[1]); !errors.Is(err, errLocalDatabase) {
			t.Fatal("nonlocal or indirect socket destination passed the final dial gate")
		}
	}
	if len(calls) != 0 {
		t.Fatal("a rejected destination reached the transport")
	}
	_, _ = dial(ctx, "tcp", "127.0.0.1:5432")
	_, _ = dial(ctx, "tcp6", "[::1]:5432")
	if !slices.Equal(calls, []string{"tcp 127.0.0.1:5432", "tcp6 [::1]:5432"}) {
		t.Fatal("unexpected loopback transport destinations")
	}
}

type schemaStatements struct {
	statements []string
	err        error
}

func (s *schemaStatements) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	return pgconn.CommandTag{}, s.err
}

func TestJournalOwnSchemaCleanup(t *testing.T) {
	ctx := context.Background()
	recorder := &schemaStatements{err: errors.New("CREATE not acknowledged")}
	nonce := [12]byte{0x12}
	_, cleanup, err := createJournalSchema(ctx, recorder, nonce)
	if err == nil || cleanup != nil || len(recorder.statements) != 1 {
		t.Fatal("cleanup registered for unsuccessful or unknown CREATE")
	}
	recorder.err = nil
	name1, cleanup1, err := createJournalSchema(ctx, recorder, nonce)
	if err != nil {
		t.Fatal(err)
	}
	nonce[11]++
	name2, cleanup2, err := createJournalSchema(ctx, recorder, nonce)
	if err != nil || name1 == name2 {
		t.Fatal("fixture names did not retain their independent identity")
	}
	if err = cleanup1(ctx); err != nil {
		t.Fatal(err)
	}
	if err = cleanup2(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(recorder.statements, []string{"CREATE SCHEMA " + name1, "CREATE SCHEMA " + name1,
		"CREATE SCHEMA " + name2, "DROP SCHEMA " + name1 + " CASCADE", "DROP SCHEMA " + name2 + " CASCADE"}) {
		t.Fatal("cleanup did not target exactly its successfully created schema")
	}
}

func TestJournalMeasuredReplayAndRunCoverage(t *testing.T) {
	writers := []writerStats{{lastMeasuredAckNS: 20}, {lastAckNS: 40}, {lastMeasuredAckNS: 25}, {lastMeasuredAckNS: 30}}
	readers := []readerStats{{firstVerifiedNS: 1}, {firstVerifiedNS: 2, firstMeasuredVerifiedNS: 29}}
	if got := readersOverlappingMeasurement(writers, readers); got != 1 {
		t.Fatal("warmup-only reader counted as overlapping the measurement")
	}
	readers[0].firstMeasuredVerifiedNS = 24
	if got := readersOverlappingMeasurement(writers, readers); got != 2 {
		t.Fatal("valid overlap on assigned shards was lost")
	}
	readers[0].firstMeasuredVerifiedNS = 26
	if got := readersOverlappingMeasurement(writers, readers); got != 1 {
		t.Fatal("a different reader's later commit was credited")
	}
	heads := inventory{RunHeads: 10000, NonemptyRuns: 9999}
	if heads.coversRuns(10000) {
		t.Fatal("unused preallocated head counted as an active run")
	}
	heads.NonemptyRuns++
	if !heads.coversRuns(10000) {
		t.Fatal("full logical run coverage rejected")
	}
}
