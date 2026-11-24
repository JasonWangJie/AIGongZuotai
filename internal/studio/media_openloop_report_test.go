package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type openLoopReportFile259 struct{ file *os.File }

func openOpenLoopReport259(t *testing.T, name string) openLoopReportFile259 {
	t.Helper()
	if !filepath.IsAbs(name) {
		t.Fatal("a new absolute local report filename is required")
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal("cannot exclusively create report")
	}
	t.Cleanup(func() {
		if f.Close() != nil {
			t.Error("could not close workload report")
		}
	})
	return openLoopReportFile259{f}
}
func (r openLoopReportFile259) write(t *testing.T, v any) {
	t.Helper()
	if err := r.append(v); err != nil {
		t.Fatal(err)
	}
}
func (r openLoopReportFile259) append(v any) error {
	if json.NewEncoder(r.file).Encode(v) != nil || r.file.Sync() != nil {
		return errors.New("aggregate report could not be persisted")
	}
	return nil
}

func (s *openLoopSeries259) terminalObservations(records []openLoopAdmission259, rows []openLoopPersisted259, seen map[string]time.Time) error {
	acks := make(map[string]time.Time, len(records))
	for _, r := range records {
		if r.outcome == "accepted" {
			acks[r.id] = r.ack
		}
	}
	for _, row := range rows {
		j := s.index(row.Updated)
		observed, ok := seen[row.ID]
		k := s.index(observed)
		if !ok || j < 0 || k < 0 || observed.Before(acks[row.ID]) {
			return errors.New("terminal commit timestamp or first independent observation outside the experiment")
		}
		s.w[j].TerminalRows++
		s.stored[j] = append(s.stored[j], row.Updated.Sub(row.Created))
		s.w[k].TerminalSeen++
		s.observed[k] = append(s.observed[k], observed.Sub(acks[row.ID]))
	}
	return nil
}

type openLoopPopulation259 struct {
	When                      time.Time
	Active, Unknown, Terminal int
}

func (s *openLoopSeries259) populations(samples []openLoopPopulation259) error {
	for _, sample := range samples {
		i := s.index(sample.When)
		if i < 0 {
			return errors.New("independent population sample outside the run")
		}
		w := &s.w[i]
		if w.PGInFlightSamples == 0 || sample.Active < w.PGLowInFlight {
			w.PGLowInFlight = sample.Active
		}
		w.PGHighInFlight = max(w.PGHighInFlight, sample.Active)
		w.PGHighUnknown = max(w.PGHighUnknown, sample.Unknown)
		w.PGHighTerminal = max(w.PGHighTerminal, sample.Terminal)
		w.PGMeanInFlight += float64(sample.Active)
		w.PGInFlightSamples++
	}
	return nil
}

func (s *openLoopSeries259) verify(total int) error {
	var planned, issued, accepted, terminal, seen int
	for i, w := range s.w {
		planned += w.Planned
		issued += w.Sent
		accepted += w.Accepted
		terminal += w.TerminalRows
		seen += w.TerminalSeen
		if w.Missed+w.HTTPRefused+w.Unknown+w.InvalidReceipts != 0 {
			return fmt.Errorf("fixed window %d has a missed arrival, refusal or unknown admission", i)
		}
		if w.AdmitLatency.Samples != w.Accepted || w.DurableCreatedToUpdated.Samples != w.TerminalRows || w.APIAckToObservedTerminal.Samples != w.TerminalSeen {
			return errors.New("window sample count differs from observations")
		}
		if w.Phase != "drain" && w.PGInFlightSamples == 0 {
			return errors.New("a scheduled window has no independent population samples")
		}
	}
	if planned != total || issued != total || accepted != total || terminal != total || seen != total {
		return errors.New("planned, admitted, persisted and observed totals differ")
	}
	return nil
}

func TestMediaOpenLoopDurableWindows259(t *testing.T) {
	p, _ := mediaOpenLoopProfile259("pilot")
	a := time.Unix(1000, 0)
	s := newOpenLoopSeries259(p, a, a.Add(p.Warm+p.Steady+3*time.Second))
	rec := openLoopAdmission259{admissionCapacityRecord: admissionCapacityRecord{id: "one"}, outcome: "accepted", due: a, sent: a, ack: a.Add(time.Millisecond)}
	rows := []openLoopPersisted259{{ID: "one", Created: a, Updated: a.Add(p.Window - time.Millisecond)}}
	if s.admissions([]openLoopAdmission259{rec}) != nil || s.terminalObservations([]openLoopAdmission259{rec}, rows, map[string]time.Time{"one": a.Add(p.Window)}) != nil {
		t.Fatal("split terminal record rejected")
	}
	if s.w[0].TerminalRows != 1 || s.w[0].TerminalSeen != 0 || s.w[1].TerminalSeen != 1 {
		t.Fatal("durable transition and first observation must have separate clocks")
	}
	if s.terminalObservations([]openLoopAdmission259{rec}, rows, nil) == nil {
		t.Fatal("missing independent observation was accepted")
	}
}

// Aggregate the fixed steady windows using the whole scheduled duration,
// independent of when the first/last successful response happened.
type openLoopSteady259 struct {
	Seconds                                                                     float64
	Planned, Missed, Sent, New202, TerminalRows, PGSamples                      int
	SendsPerSecond, New202PerSecond, TerminalRowsPerSecond, MeanSampledInFlight float64
	Admission, DurableCreatedToUpdated, APIAckToObservedTerminal                openLoopLatency259
}

func (s *openLoopSeries259) steady() openLoopSteady259 {
	out := openLoopSteady259{Seconds: s.p.Steady.Seconds()}
	var admissions, stored, seen []time.Duration
	for i, w := range s.w {
		if w.Phase != "steady" {
			continue
		}
		out.Planned += w.Planned
		out.Missed += w.Missed
		out.Sent += w.Sent
		out.New202 += w.Accepted
		out.TerminalRows += w.TerminalRows
		out.MeanSampledInFlight += w.PGMeanInFlight * float64(w.PGInFlightSamples)
		out.PGSamples += w.PGInFlightSamples
		admissions = append(admissions, s.admit[i]...)
		stored = append(stored, s.stored[i]...)
		seen = append(seen, s.observed[i]...)
	}
	if out.PGSamples != 0 {
		out.MeanSampledInFlight /= float64(out.PGSamples)
	}
	out.SendsPerSecond = float64(out.Sent) / out.Seconds
	out.New202PerSecond = float64(out.New202) / out.Seconds
	out.TerminalRowsPerSecond = float64(out.TerminalRows) / out.Seconds
	out.Admission = openLoopLatencies259(admissions)
	out.DurableCreatedToUpdated = openLoopLatencies259(stored)
	out.APIAckToObservedTerminal = openLoopLatencies259(seen)
	return out
}
