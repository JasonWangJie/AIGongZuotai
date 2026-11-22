package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The applicant sees only the outcome of the linked case. Internal ticket
// references, evidence, personnel and wallet totals never enter this projection.
type ContentAppealFinanceStatus struct {
	Status         string `json:"status"`
	ReceiptID      string `json:"receipt_id,omitempty"`
	ReleasedMicros string `json:"released_micros,omitempty"`
}

type appealFinanceLink struct {
	ContentAppealFinanceStatus
	AppealID string `json:"appeal_id"`
	CaseID   string `json:"case_id"`
}

func readAppealFinanceLinkSQL(ctx context.Context, tx pgx.Tx, workspace, appealID, caseID, actor string, keyHash []byte) (appealFinanceLink, error) {
	var v appealFinanceLink
	if (appealID == "") == (caseID == "") {
		return v, pgx.ErrNoRows
	}
	column, id := "l.appeal_id", appealID
	if caseID != "" {
		column, id = "l.case_id", caseID
	}
	var caseState, action, receiptID string
	var released int64
	// Only the immutable journal, original bound reservation, consumed operation
	// and formal case receipt together establish that CREDIT was made available.
	err := tx.QueryRow(ctx, `SELECT a.id,c.id,c.state,coalesce(o.document->>'action',''),coalesce(o.id,''),coalesce(j.amount_micros,0)
		FROM studio_content_appeal_finance_links l
		JOIN studio_content_appeals a ON a.id=l.appeal_id
		JOIN studio_operations_cases c ON c.id=l.case_id AND c.workspace_id=a.workspace_id AND c.project_id=a.project_id AND c.resource_type=a.resource_type AND c.resource_id=a.resource_id
		LEFT JOIN studio_operations_receipts o ON o.workspace_id=c.workspace_id AND o.case_id=c.id
		LEFT JOIN studio_ledger_operations m ON m.operation_key='ops:'||o.plan_id AND m.purpose='resolve' AND m.response->'reservation'->>'reservation_id'=l.reservation_id
		LEFT JOIN studio_ledger_journals j ON j.reservation_id=l.reservation_id AND j.kind='RELEASE' AND j.workspace_id=a.workspace_id AND j.resource_id=a.resource_id AND j.project_id=a.project_id AND j.resource_type=a.resource_type AND j.available_delta_micros=j.amount_micros AND m.response->'reservation'->>'released_micros'=j.amount_micros::text AND m.response->'reservation'->>'state'='RELEASED'
		WHERE a.workspace_id=$1 AND `+column+`=$2
		AND ($3='' OR (l.created_by=$3 AND l.key_hash=$4))`, workspace, id, actor, keyHash).Scan(&v.AppealID, &v.CaseID, &caseState, &action, &receiptID, &released)
	if err != nil {
		return v, err
	}
	v.Status = "REFERRED"
	if caseState == "RESOLVED" {
		v.Status = "RESOLVED_OTHER"
		if action == "release" && released > 0 {
			v.Status = "RELEASED"
			v.ReceiptID = receiptID
			v.ReleasedMicros = strconv.FormatInt(released, 10)
		}
	}
	return v, nil
}

func readContentAppealFinanceStatusSQL(ctx context.Context, tx pgx.Tx, workspace, id string) (*ContentAppealFinanceStatus, error) {
	link, err := readAppealFinanceLinkSQL(ctx, tx, workspace, id, "", "", nil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &link.ContentAppealFinanceStatus, nil
}

type appealFinanceItem struct {
	AppealID     string    `json:"appeal_id"`
	ProjectID    string    `json:"project_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Version      uint64    `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	CaseID       string    `json:"case_id,omitempty"`
}

func (a *App) contentAppealFinanceQueueHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "finance")
	if err != nil {
		writeError(w, err)
		return
	}
	cursor, limit, err := parseOperationsPage(r, p, workspace, "content-appeals-finance", "referred")
	if err != nil {
		writeError(w, err)
		return
	}
	result := struct {
		Items []appealFinanceItem `json:"items"`
		Next  *string             `json:"next_cursor"`
	}{Items: []appealFinanceItem{}}
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "finance"); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT a.id,a.project_id,a.resource_type,a.resource_id,a.version,a.created_at,coalesce(l.case_id,'') FROM studio_content_appeals a
		LEFT JOIN studio_content_appeal_finance_links l ON l.appeal_id=a.id
		WHERE a.workspace_id=$1 AND a.outcome='FINANCE_REFERRED' AND ($2='' OR (a.created_at,a.id)<($3,$2))
		ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, workspace, cursor.ID, cursor.Before, limit+1)
		if e != nil {
			return e
		}
		items, e := pgx.CollectRows(rows, pgx.RowToStructByPos[appealFinanceItem])
		if e != nil {
			return e
		}
		if len(items) > limit {
			items = items[:limit]
			last := items[limit-1]
			cursor.ID = last.AppealID
			cursor.Before = last.CreatedAt
			result.Next = nextOperationsCursor(cursor)
		}
		if len(items) > 0 {
			result.Items = items
		}
		return tx.Commit(ctx)
	})
	contentAppealResponse(w, result, 200, err)
}

func (a *App) contentAppealFinanceReadHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace_id")
	p, err := a.requireOperations(r, workspace, "finance")
	if err != nil {
		writeError(w, err)
		return
	}
	if id := r.PathValue("id"); id != "" && !validOperationsID(id, "capl") {
		writeError(w, operationsNotFound())
		return
	}
	if id := r.PathValue("case"); id != "" && !validOperationsID(id, "ocase") {
		writeError(w, operationsNotFound())
		return
	}
	var actor string
	var hash []byte
	if r.Header.Get("Idempotency-Key") != "" {
		key, e := idempotencyKey(r)
		if e != nil {
			writeError(w, e)
			return
		}
		h := sha256.Sum256([]byte(key))
		hash = h[:]
		actor = p.User.ID
	}
	var result appealFinanceLink
	err = a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, workspace, "finance"); e != nil {
			return e
		}
		result, e = readAppealFinanceLinkSQL(ctx, tx, workspace, r.PathValue("id"), r.PathValue("case"), actor, hash)
		if errors.Is(e, pgx.ErrNoRows) {
			return operationsNotFound()
		}
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	contentAppealResponse(w, result, 200, err)
}

type appealFinanceLinkInput struct {
	WorkspaceID     string `json:"workspace_id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

func (a *App) contentAppealFinanceLinkHTTP(w http.ResponseWriter, r *http.Request) {
	var in appealFinanceLinkInput
	if err := decodeGenerationJSON(w, r, &in, "workspace_id", "expected_version"); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.requireOperations(r, in.WorkspaceID, "finance")
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, replayed, err := a.linkContentAppealFinance(p, r.PathValue("id"), in, key)
	if replayed && err == nil {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	contentAppealResponse(w, result, 201, err)
}

func appealFinanceChanged() error {
	return failure(409, "CONTENT_APPEAL_FINANCE_CHANGED", "原申诉、财务工单或可核实的冻结已变化，请重新读取")
}

func (a *App) linkContentAppealFinance(p *principal, id string, in appealFinanceLinkInput, key string) (appealFinanceLink, bool, error) {
	if !validOperationsID(id, "capl") {
		return appealFinanceLink{}, false, operationsNotFound()
	}
	keyHash := sha256.Sum256([]byte(key))
	requestHash := payloadHash(struct {
		AppealID string
		Input    appealFinanceLinkInput
	}{id, in})
	var result appealFinanceLink
	var replay bool
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.store.do(func(ctx context.Context, c *pgx.Conn) error {
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		defer tx.Rollback(ctx)
		if _, e = authorizeOperationsSQL(ctx, tx, p, in.WorkspaceID, "finance"); e != nil {
			return e
		}
		var saved []byte
		e = tx.QueryRow(ctx, "SELECT request_hash FROM studio_content_appeal_finance_links WHERE created_by=$1 AND key_hash=$2", p.User.ID, keyHash[:]).Scan(&saved)
		if e == nil {
			if string(saved) != string(requestHash[:]) {
				return failure(409, "IDEMPOTENCY_CONFLICT", "该键已用于不同财务转交请求")
			}
			result, e = readAppealFinanceLinkSQL(ctx, tx, in.WorkspaceID, id, "", p.User.ID, keyHash[:])
			if e != nil {
				return e
			}
			replay = true
			return tx.Commit(ctx)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var resourceKind, resourceID, project, reservationID string
		var reservationVersion, version uint64
		var remaining int64
		var linked bool
		e = tx.QueryRow(ctx, `SELECT resource_type,resource_id,project_id,reservation_id,reservation_version,remaining_micros,version,
			EXISTS(SELECT 1 FROM studio_content_appeal_finance_links WHERE appeal_id=a.id)
			FROM studio_content_appeals a WHERE id=$1 AND workspace_id=$2 AND state='RESOLVED' AND outcome='FINANCE_REFERRED' FOR UPDATE OF a`, id, in.WorkspaceID).Scan(&resourceKind, &resourceID, &project, &reservationID, &reservationVersion, &remaining, &version, &linked)
		if errors.Is(e, pgx.ErrNoRows) {
			return operationsNotFound()
		}
		if e != nil {
			return e
		}
		if version != in.ExpectedVersion || linked {
			return appealFinanceChanged()
		}
		// Follow the same lock order as the original case resolution: existing
		// case first, then the underlying resource and its original reservation.
		var caseID string
		e = tx.QueryRow(ctx, `SELECT id FROM studio_operations_cases WHERE workspace_id=$1 AND project_id=$2 AND resource_type=$3 AND resource_id=$4 AND state='OPEN' FOR UPDATE`, in.WorkspaceID, project, resourceKind, resourceID).Scan(&caseID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		resource, e := a.readOperationsResourceSQL(ctx, tx, in.WorkspaceID, resourceKind, resourceID, true)
		if e != nil {
			return e
		}
		b := resource.Summary.Billing
		if b == nil || resource.Summary.ProjectID != project || !terminal(resource.Summary.Status) || b.ReservationID != reservationID {
			return appealFinanceChanged()
		}
		row, e := readLedgerReservationSQL(ctx, tx, reservationID, true)
		if e != nil {
			return e
		}
		if row.WorkspaceID != in.WorkspaceID || row.ProjectID != project || row.ResourceType != resourceKind || row.ResourceID != resourceID || row.State != "REVIEW" || row.Version != reservationVersion || remaining <= 0 || remaining != row.ReservedMicros-row.ReleasedMicros-row.ChargedMicros {
			return appealFinanceChanged()
		}
		if caseID == "" {
			created, e := insertOperationsCaseSQL(ctx, tx, p, OperationsCaseInput{WorkspaceID: in.WorkspaceID, ResourceType: resourceKind, ResourceID: resourceID, ResourceVersion: resource.Summary.ResourceVersion, Reason: "billing_review", EvidenceRef: id}, resource)
			if e != nil {
				return e
			}
			caseID = created.ID
		}
		_, e = tx.Exec(ctx, `INSERT INTO studio_content_appeal_finance_links(appeal_id,case_id,reservation_id,reservation_version,created_by,key_hash,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, caseID, reservationID, reservationVersion, p.User.ID, keyHash[:], requestHash[:])
		if e != nil {
			return e
		}
		if e = insertOperationsAuditSQL(ctx, tx, OperationsAuditEvent{ActorUserID: p.User.ID, WorkspaceID: in.WorkspaceID, Action: "content_appeal.finance_linked", CaseID: caseID, ResourceType: "appeal", ResourceID: id, ReasonCode: "billing_review", Version: version}); e != nil {
			return e
		}
		result, e = readAppealFinanceLinkSQL(ctx, tx, in.WorkspaceID, id, "", "", nil)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	return result, replay, err
}
