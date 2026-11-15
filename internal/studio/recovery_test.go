package studio

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresRecoveryBasePageByteBoundary(t *testing.T) {
	conn, err := pgx.Connect(context.Background(), testDatabase(t))
	if err != nil {
		t.Fatal("fixture database unavailable")
	}
	defer conn.Close(context.Background())
	// Server-generated large rows test transfer gating without persisting
	// invalid user resources or treating these bytes as accepted API input.
	source := `SELECT lpad(i::text,4,'0') AS id,jsonb_build_object('value',repeat('x',2097152)) AS body FROM generate_series(1,10) i WHERE lpad(i::text,4,'0')>$1 ORDER BY id LIMIT $2`
	page, used, err := readRecoveryRows(context.Background(), conn, source, "", recoveryMediaPageRecords)
	if err != nil || len(page) != 7 || used > recoveryMediaPageBytes || used < 14<<20 {
		t.Fatalf("byte bounded page rows=%d bytes=%d error=%v", len(page), used, err)
	}
	if page[6].id != "0007" {
		t.Fatal("byte overflow advanced the cursor")
	}
	next, _, err := readRecoveryRows(context.Background(), conn, source, page[6].id, recoveryMediaPageRecords)
	if err != nil || len(next) != 3 || next[0].id != "0008" || next[2].id != "0010" {
		t.Fatal("overflow row lost on the next page", err)
	}
	source = `SELECT 'oversized'::text AS id,jsonb_build_object('value',repeat('x',16777216)) AS body WHERE 'oversized'>$1 LIMIT $2`
	if page, _, err = readRecoveryRows(context.Background(), conn, source, "", recoveryMediaPageRecords); err == nil || len(page) != 0 {
		t.Fatal("oversized first record silently skipped")
	}
}
