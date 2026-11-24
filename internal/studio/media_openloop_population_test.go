package studio

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read one independent PostgreSQL snapshot for totals and newly observed
// terminal IDs. The driver's balance of sent requests is never the population.
type openLoopPGPopulation259 struct {
	samples       []openLoopPopulation259
	firstTerminal map[string]time.Time
	seenIDs       []string
}

func (p *openLoopPGPopulation259) sample(ctx context.Context, db *pgx.Conn) error {
	stmt := `WITH workload_rows AS MATERIALIZED (SELECT id,document->>'status' AS status FROM studio_tasks)
	SELECT count(*) FILTER(WHERE status NOT IN ('FAILED','SUCCEEDED','PARTIAL','CANCELLED','SUBMIT_UNKNOWN','RECONCILE_REQUIRED')),
	count(*) FILTER(WHERE status IN ('SUBMIT_UNKNOWN','RECONCILE_REQUIRED')),
	count(*) FILTER(WHERE status IN ('FAILED','SUCCEEDED','PARTIAL','CANCELLED')),
	ARRAY(SELECT id FROM workload_rows WHERE status IN ('FAILED','SUCCEEDED','PARTIAL','CANCELLED') AND NOT(id=ANY($1::text[]))) FROM workload_rows`
	if p.firstTerminal == nil {
		p.firstTerminal = make(map[string]time.Time)
		p.seenIDs = []string{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var sample openLoopPopulation259
	var ids []string
	if err := db.QueryRow(ctx, stmt, p.seenIDs).Scan(&sample.Active, &sample.Unknown, &sample.Terminal, &ids); err != nil {
		return errors.New("independent durable population query failed")
	}
	sample.When = time.Now()
	for _, id := range ids {
		if _, seen := p.firstTerminal[id]; seen {
			return errors.New("duplicate terminal observation")
		}
		p.firstTerminal[id] = sample.When
		p.seenIDs = append(p.seenIDs, id)
	}
	if len(p.firstTerminal) != sample.Terminal {
		return errors.New("terminal population decreased or was omitted")
	}
	p.samples = append(p.samples, sample)
	return nil
}
