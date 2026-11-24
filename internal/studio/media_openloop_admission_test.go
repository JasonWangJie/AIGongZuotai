package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type openLoopProfile259 struct {
	Name                                                                string
	Warm, Steady, Life, Poll, Handler, Slot, Window, Sample, DrainLimit time.Duration
}

func mediaOpenLoopProfile259(name string) (openLoopProfile259, bool) {
	p := openLoopProfile259{Name: name, Slot: time.Second, Handler: 200 * time.Millisecond, Sample: time.Second}
	switch name {
	case "pilot":
		p.Warm, p.Steady, p.Life = 12*time.Second, 12*time.Second, 12*time.Second
		p.Poll, p.Window, p.DrainLimit = 3*time.Second, 4*time.Second, 80*time.Second
	case "1ps":
		p.Warm, p.Steady, p.Life = 10*time.Minute, 10*time.Minute, 10*time.Minute
		p.Poll, p.Window, p.DrainLimit = 10*time.Second, time.Minute, 13*time.Minute
	default:
		return openLoopProfile259{}, false
	}
	return p, true
}
func (p openLoopProfile259) slots() int { return int((p.Warm + p.Steady) / p.Slot) }

type openLoopAdmission259 struct {
	admissionCapacityRecord
	due, deadline, sent, ack time.Time
	rowCreated               time.Time
	outcome                  string
	code                     int
}

func mediaOpenLoopAdmit259(ctx context.Context, api string, account admissionCapacityAccount, record *openLoopAdmission259) {
	raw, err := json.Marshal(record.input)
	if err != nil {
		record.outcome = "driver_error"
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/api/v1/tasks", bytes.NewReader(raw))
	if err != nil {
		record.outcome = "driver_error"
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-ID", account.identity.Project.ID)
	req.Header.Set("Idempotency-Key", record.key)
	sent := time.Now()
	if !sent.Before(record.deadline) {
		record.outcome = "expired_slot"
		return
	}
	record.sent = sent
	res, err := account.client.Do(req)
	if err != nil {
		record.outcome = "response_unknown"
		return
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, (96<<10)+1))
	record.ack = time.Now()
	record.code = res.StatusCode
	if err != nil || len(raw) > 96<<10 {
		record.outcome = "response_unknown"
		return
	}
	if res.StatusCode != http.StatusAccepted {
		switch res.StatusCode {
		case 400, 401, 403, 404, 409, 413, 422, 429:
			record.outcome = "http_refusal"
		default:
			record.outcome = "response_unknown"
		}
		return
	}
	var task Task
	if json.Unmarshal(raw, &task) != nil || task.ID == "" || task.CreatedAt.IsZero() || task.ProjectID != record.input.ProjectID || task.WorkspaceID != account.identity.Workspace.ID || task.ProviderMode != "live" || task.Version != 1 || res.Header.Get("Idempotency-Replayed") != "" {
		record.outcome = "invalid_receipt"
		return
	}
	record.id, record.rowCreated, record.outcome = task.ID, task.CreatedAt, "accepted"
}

func openLoopWait259(ctx context.Context, when time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	delay := time.Until(when)
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func openLoopSlots259(ctx context.Context, p openLoopProfile259, start time.Time, accounts []admissionCapacityAccount, api string, records []openLoopAdmission259) error {
	const maxRequests = 4
	permits := make(chan struct{}, maxRequests)
	var wg sync.WaitGroup
	defer wg.Wait()
	for i := range records {
		record := &records[i]
		record.owner = i % len(accounts)
		record.due = start.Add(time.Duration(i) * p.Slot)
		record.deadline = record.due.Add(p.Slot)
		record.input = taskInput("image")
		record.input.ProjectID = accounts[record.owner].identity.Project.ID
		record.input.Inputs.Prompt = openLoopPrompt259(i)
		record.key = fmt.Sprintf("openloop-259-%d", i)
		if !openLoopWait259(ctx, record.due) {
			return errors.New("load interrupted before the last slot")
		}
		if !time.Now().Before(record.deadline) {
			record.outcome = "expired_slot"
			continue
		}
		select {
		case permits <- struct{}{}:
			wg.Add(1)
			go func(a admissionCapacityAccount) {
				defer wg.Done()
				defer func() { <-permits }()
				mediaOpenLoopAdmit259(ctx, api, a, record)
			}(accounts[record.owner])
		default:
			record.outcome = "sender_limit"
		}
	}
	if !openLoopWait259(ctx, start.Add(p.Warm+p.Steady)) {
		return errors.New("load ended before the fixed measurement boundary")
	}
	return nil
}

type openLoopLatency259 struct {
	Samples                            int `json:"samples"`
	MeanMS, P50MS, P95MS, P99MS, MaxMS float64
}

func openLoopLatencies259(samples []time.Duration) openLoopLatency259 {
	if len(samples) == 0 {
		return openLoopLatency259{}
	}
	d := queryCapacityPercentiles(samples)
	return openLoopLatency259{len(samples), d.MeanMS, d.P50MS, d.P95MS, d.P99MS, d.MaxMS}
}

type openLoopBucket259 struct {
	Phase                                                                                                         string
	StartMS, EndMS                                                                                                int64
	Planned, Missed, ExpiredSlots, SenderFullSlots, Sent, Accepted, HTTPRefused, API429, Unknown, InvalidReceipts int
	TerminalRows, TerminalSeen, PGInFlightSamples, PGLowInFlight, PGHighInFlight                                  int
	PGHighUnknown, PGHighTerminal                                                                                 int
	PGMeanInFlight                                                                                                float64
	AdmitLatency, DurableCreatedToUpdated, APIAckToObservedTerminal                                               openLoopLatency259
}
type openLoopSeries259 struct {
	p                       openLoopProfile259
	start, end              time.Time
	w                       []openLoopBucket259
	admit, stored, observed [][]time.Duration
}

func newOpenLoopSeries259(p openLoopProfile259, start, end time.Time) *openLoopSeries259 {
	n := int((end.Sub(start) + p.Window - 1) / p.Window)
	s := &openLoopSeries259{p: p, start: start, end: end, w: make([]openLoopBucket259, n), admit: make([][]time.Duration, n), stored: make([][]time.Duration, n), observed: make([][]time.Duration, n)}
	for i := range s.w {
		d := time.Duration(i) * p.Window
		phase := "warmup"
		if d >= p.Warm+p.Steady {
			phase = "drain"
		} else if d >= p.Warm {
			phase = "steady"
		}
		s.w[i] = openLoopBucket259{Phase: phase, StartMS: d.Milliseconds(), EndMS: min(end.Sub(start), d+p.Window).Milliseconds()}
	}
	return s
}
func (s *openLoopSeries259) index(at time.Time) int {
	if at.Before(s.start) || at.After(s.end) {
		return -1
	}
	return min(int(at.Sub(s.start)/s.p.Window), len(s.w)-1)
}
func (s *openLoopSeries259) admissions(records []openLoopAdmission259) error {
	for _, r := range records {
		i := s.index(r.due)
		if i < 0 {
			return errors.New("slot outside schedule")
		}
		s.w[i].Planned++
		if r.outcome == "expired_slot" || r.outcome == "sender_limit" {
			s.w[i].Missed++
			if r.outcome == "expired_slot" {
				s.w[i].ExpiredSlots++
			} else {
				s.w[i].SenderFullSlots++
			}
			continue
		}
		if !r.sent.IsZero() {
			j := s.index(r.sent)
			if j < 0 {
				return errors.New("send outside observation")
			}
			s.w[j].Sent++
		}
		at := r.ack
		if at.IsZero() {
			at = r.sent
		}
		if at.IsZero() {
			at = r.due
		}
		j := s.index(at)
		if j < 0 {
			return errors.New("receipt outside observation")
		}
		switch r.outcome {
		case "accepted":
			s.w[j].Accepted++
			s.admit[j] = append(s.admit[j], r.ack.Sub(r.sent))
		case "http_refusal":
			s.w[j].HTTPRefused++
			if r.code == http.StatusTooManyRequests {
				s.w[j].API429++
			}
		case "response_unknown":
			s.w[j].Unknown++
		default:
			s.w[j].InvalidReceipts++
		}
	}
	return nil
}
func (s *openLoopSeries259) finish() {
	for i := range s.w {
		w := &s.w[i]
		w.AdmitLatency = openLoopLatencies259(s.admit[i])
		w.DurableCreatedToUpdated = openLoopLatencies259(s.stored[i])
		w.APIAckToObservedTerminal = openLoopLatencies259(s.observed[i])
		if w.PGInFlightSamples != 0 {
			w.PGMeanInFlight /= float64(w.PGInFlightSamples)
		}
	}
}

func TestMediaOpenLoopSchedules259(t *testing.T) {
	p, ok := mediaOpenLoopProfile259("1ps")
	if !ok || p.slots() != 1200 || p.Life != 600*time.Second || p.Warm < p.Life || p.Steady < p.Life || p.Handler != 200*time.Millisecond || p.Poll != 10*time.Second {
		t.Fatal("full load profile drift")
	}
	if _, ok = mediaOpenLoopProfile259("100ps"); ok {
		t.Fatal("unqualified deployment profile")
	}
	p, _ = mediaOpenLoopProfile259("pilot")
	start := time.Unix(1000, 0)
	end := start.Add(p.Warm + p.Steady + 1500*time.Millisecond)
	s := newOpenLoopSeries259(p, start, end)
	if s.w[3].Phase != "steady" || s.w[6].Phase != "drain" || s.index(end) != 6 || s.index(end.Add(time.Nanosecond)) != -1 {
		t.Fatal("wall-clock phase or partial interval")
	}
	input := []openLoopAdmission259{{due: start, sent: start.Add(p.Window - time.Nanosecond), ack: start.Add(p.Window), outcome: "accepted"}, {due: start.Add(p.Warm), outcome: "sender_limit"}}
	if s.admissions(input) != nil {
		t.Fatal("split send/ack and missed arrival were rejected")
	}
	s.finish()
	if s.w[0].Planned != 1 || s.w[0].Sent != 1 || s.w[0].Accepted != 0 || s.w[1].Accepted != 1 || s.w[1].AdmitLatency.Samples != 1 || s.w[3].Missed != 1 {
		t.Fatal("window counted an unobserved acceptance")
	}
}

func TestMediaOpenLoopBoundedAdmission259(t *testing.T) {
	p, _ := mediaOpenLoopProfile259("pilot")
	p.Slot = 30 * time.Millisecond
	p.Warm = 4 * p.Slot
	p.Steady = 4 * p.Slot
	start := time.Now().Add(20 * time.Millisecond)
	var sent atomic.Int32
	release := make(chan struct{})
	released := time.AfterFunc(time.Until(start.Add(p.Warm+p.Steady))+35*time.Millisecond, func() { close(release) })
	defer released.Stop()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != "/api/v1/tasks" || req.Header.Get("X-Project-ID") != "test-project" || req.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(400)
			return
		}
		id := sent.Add(1)
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(Task{ID: fmt.Sprintf("task-%d", id), WorkspaceID: "test-workspace", ProjectID: "test-project", Version: 1, ProviderMode: "live", CreatedAt: time.Now()})
	}))
	defer server.Close()
	account := admissionCapacityAccount{client: server.Client()}
	account.identity.Project.ID = "test-project"
	account.identity.Workspace.ID = "test-workspace"
	records := make([]openLoopAdmission259, p.slots())
	if e := openLoopSlots259(t.Context(), p, start, []admissionCapacityAccount{account}, server.URL, records); e != nil {
		t.Fatal(e)
	}
	var accepted, missed int
	for _, r := range records {
		switch r.outcome {
		case "accepted":
			accepted++
		case "sender_limit", "expired_slot":
			missed++
		default:
			t.Fatal("unexpected admission outcome")
		}
	}
	if accepted != int(sent.Load()) || accepted == 0 || accepted > 4 || missed < 4 || accepted+missed != len(records) {
		t.Fatal("backpressure did not record the unissued arrivals")
	}
}

func TestMediaOpenLoopUncertainAdmission259(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	acc := admissionCapacityAccount{client: server.Client()}
	acc.identity.Project.ID = "project"
	rec := openLoopAdmission259{admissionCapacityRecord: admissionCapacityRecord{key: "test-key", input: taskInput("image")}, deadline: time.Now().Add(time.Second)}
	mediaOpenLoopAdmit259(t.Context(), server.URL, acc, &rec)
	if calls.Load() != 1 || rec.outcome != "response_unknown" || rec.key != "test-key" {
		t.Fatal("unconfirmed response was retried or treated as a refusal")
	}
}

func openLoopAdmissionOutcomes259(rows []openLoopAdmission259) map[string]int {
	out := make(map[string]int)
	for _, r := range rows {
		key := r.outcome
		if key == "" {
			key = "remaining_when_load_stopped"
		}
		out[key]++
	}
	return out
}
