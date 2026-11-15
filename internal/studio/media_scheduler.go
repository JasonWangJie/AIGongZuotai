package studio

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const mediaScheduleWorkspacePage = 16
const mediaScheduleTaskPage = 8

type MediaSchedulerLaneSnapshot struct {
	ScopeInFlight      int
	CapacityDeferrals  uint64
	CooldownDeferrals  uint64
	ExaminedCandidates uint64
	Claims             uint64
}
type MediaSchedulerSnapshot struct{ Lanes [4]MediaSchedulerLaneSnapshot }

type mediaTaskSchedule struct {
	TaskID, WorkspaceID, ProviderMode, Lane, ChannelKey string
	ServerNotBefore                                     time.Time
	EligibleAt                                          time.Time
	FailureCount, UnchangedCount, LastProgress          int
	Active                                              bool
	Version                                             int64
	LastObservationID                                   string
}
type mediaScheduleCandidate struct {
	TaskID, WorkspaceID, ProviderMode, ChannelKey string
	Lane                                          mediaExecutionLane
	ProjectionVersion                             int64
	ChannelVersion                                int64
	EffectiveLimit                                int
	HardLimit                                     int
	ProbeID                                       string
}
type mediaScheduleOutcome struct {
	ID             string
	At             time.Time
	Lane           mediaExecutionLane
	ChannelKey     string
	Observation    *MediaScheduleObservation
	Temporary      bool
	Progress       int
	BaseInterval   time.Duration
	ServerInterval time.Duration
}
type mediaChannelRuntime struct {
	CooldownUntil                              time.Time
	Parked                                     bool
	FailureCount, SuccessCount, EffectiveLimit int
	Version                                    int64
	ProbeRequired                              bool
	ProbeID                                    string
	ProbeUntil                                 *time.Time
}
type mediaScheduler struct {
	mu              sync.Mutex
	config          MediaSchedulingConfig
	workspace       [4]map[string]int
	channel         [4]map[string]int
	stats           MediaSchedulerSnapshot
	workspaceCursor [4]string
	taskCursor      [4]map[string]string
	memoryChannels  [4]map[string]mediaChannelRuntime
}

func newMediaScheduler(c MediaSchedulingConfig) *mediaScheduler {
	_ = c.normalize(4)
	s := &mediaScheduler{config: c}
	for i := range 4 {
		s.workspace[i] = map[string]int{}
		s.channel[i] = map[string]int{}
		s.taskCursor[i] = map[string]string{}
		s.memoryChannels[i] = map[string]mediaChannelRuntime{}
	}
	return s
}
func mediaLaneName(l mediaExecutionLane) string {
	if l > mediaReviewLane {
		return ""
	}
	return [...]string{"submit", "query", "delivery", "review"}[l]
}
func mediaLaneFromName(s string) mediaExecutionLane {
	switch s {
	case "submit":
		return mediaSubmitLane
	case "query":
		return mediaQueryLane
	case "delivery":
		return mediaDeliveryLane
	case "review":
		return mediaReviewLane
	}
	return 255
}
func mediaScheduleScope(l mediaExecutionLane) string {
	switch l {
	case mediaReviewLane:
		return "review"
	case mediaDeliveryLane:
		return "source"
	default:
		return "gateway"
	}
}
func (s *mediaScheduler) laneLimit(l mediaExecutionLane) int {
	return [...]int{s.config.SubmitWorkers, s.config.QueryWorkers, s.config.DeliveryWorkers, s.config.ReviewWorkers}[l]
}
func (s *mediaScheduler) tryReserve(c mediaScheduleCandidate) (func(), bool) {
	if s == nil || c.Lane > mediaReviewLane {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := c.Lane
	cl := min(s.config.ChannelConcurrency, s.laneLimit(l))
	if c.EffectiveLimit > 0 {
		cl = min(cl, c.EffectiveLimit)
	}
	if s.workspace[l][c.WorkspaceID] >= min(s.config.WorkspaceConcurrency, s.laneLimit(l)) || s.channel[l][c.ChannelKey] >= cl {
		s.stats.Lanes[l].CapacityDeferrals++
		return nil, false
	}
	s.workspace[l][c.WorkspaceID]++
	s.channel[l][c.ChannelKey]++
	s.stats.Lanes[l].ScopeInFlight++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.workspace[l][c.WorkspaceID]--
			s.channel[l][c.ChannelKey]--
			s.stats.Lanes[l].ScopeInFlight--
			if s.workspace[l][c.WorkspaceID] == 0 {
				delete(s.workspace[l], c.WorkspaceID)
			}
			if s.channel[l][c.ChannelKey] == 0 {
				delete(s.channel[l], c.ChannelKey)
			}
		})
	}, true
}
func (s *mediaScheduler) snapshot() MediaSchedulerSnapshot {
	if s == nil {
		return MediaSchedulerSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
func (s *mediaScheduler) examined(l mediaExecutionLane, cold bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Lanes[l].ExaminedCandidates++
	if cold {
		s.stats.Lanes[l].CooldownDeferrals++
	}
}
func (s *mediaScheduler) claimed(l mediaExecutionLane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Lanes[l].Claims++
}

func mediaLegacyChannel(mode string, l mediaExecutionLane) string {
	h := md5.Sum([]byte("media-legacy-v1:" + mode + ":" + mediaLaneName(l)))
	s := hex.EncodeToString(h[:])
	return s + s
}
func mediaFrozenChannel(t *taskRecord, l mediaExecutionLane) (string, error) {
	if l == mediaReviewLane && t.MediaModeration != nil && t.MediaModeration.Policy != nil {
		return t.MediaModeration.Policy.AdapterIdentity, nil
	}
	var submission string
	if t.Submission != nil {
		submission = t.Submission.ProviderIdentity
	}
	if submission != "" && t.Handle.ProviderIdentity != "" && submission != t.Handle.ProviderIdentity {
		return "", errors.New("media frozen channel identity differs")
	}
	key := submission
	if key == "" {
		key = t.Handle.ProviderIdentity
	}
	if key == "" {
		return mediaLegacyChannel(t.Task.ProviderMode, l), nil
	}
	if !moderationDigestPattern.MatchString(key) {
		// Older/custom adapters used opaque identities. Preserve their original
		// bytes for the adapter and share a conservative legacy budget; this is
		// not evidence that a native credential matches today's configuration.
		return mediaLegacyChannel(t.Task.ProviderMode, l), nil
	}
	return key, nil
}
func cloneMediaTaskSchedule(s *mediaTaskSchedule) *mediaTaskSchedule {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}
func mediaScheduleTimestamp(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	rounded := t.UTC().Truncate(time.Microsecond)
	if rounded.Before(t) {
		rounded = rounded.Add(time.Microsecond)
	}
	return rounded
}
func mergeMediaScheduleObservations(a, b *MediaScheduleObservation) *MediaScheduleObservation {
	if a == nil {
		if b == nil {
			return nil
		}
		c := *b
		return &c
	}
	if b == nil {
		return a
	}
	c := *a
	rank := func(s string) int {
		switch s {
		case "rate_limit":
			return 5
		case "temporary":
			return 4
		case "protocol":
			return 3
		case "identity":
			return 2
		case "cancelled":
			return 1
		}
		return 0
	}
	if rank(b.Class) > rank(c.Class) {
		c.Class = b.Class
		c.Scope = b.Scope
	}
	c.HasServerDelay = c.HasServerDelay || b.HasServerDelay
	c.RetryNotBefore = maxTime(c.RetryNotBefore, b.RetryNotBefore)
	c.DelayExceeded = c.DelayExceeded || b.DelayExceeded
	return &c
}
func applyMediaScheduleExceeded(t *taskRecord) {
	if t.ScheduleOutcome == nil || t.ScheduleOutcome.Observation == nil || !t.ScheduleOutcome.Observation.DelayExceeded || terminal(t.Task.Status) || t.Task.Status == "SUBMIT_UNKNOWN" || t.Task.Status == "RECONCILE_REQUIRED" {
		return
	}
	t.Task.Status = "RECONCILE_REQUIRED"
	t.Task.Error = &APIError{Code: "MEDIA_RETRY_WINDOW_EXCEEDED", Message: "服务要求的等待超出自动恢复范围，原任务保持待核实"}
	t.Task.CanReconcile = false
	if !t.ScheduleOutcome.Observation.RetryNotBefore.IsZero() {
		deadline := t.ScheduleOutcome.Observation.RetryNotBefore
		t.Task.ReconcileAfter = &deadline
	}
	// No result exists yet: keep the original input policy and allow an explicit
	// original-handle GET after the exact server deadline. Existing candidates
	// retain the stronger no-reconcile/no-recheck quarantine semantics.
	if len(t.MediaModerationCandidates()) > 0 {
		quarantineMediaTask(t, "execution_incomplete")
	}
}
func (a *App) observeMediaStep(t *taskRecord, result MediaResult, err error) {
	l := mediaLaneForTask(t)
	key, _ := mediaFrozenChannel(t, l)
	if t.ScheduleClaim != nil {
		l = t.ScheduleClaim.Lane
		key = t.ScheduleClaim.ChannelKey
	}
	o := result.Observation
	if o == nil && l == mediaSubmitLane {
		o = t.Handle.Observation
	}
	var e *MediaProviderError
	temporary := false
	if errors.As(err, &e) {
		o = e.Observation
		temporary = e.Retryable && !e.SubmissionUnknown && !e.ReconcileRequired
	}
	interval := result.RetryAfter
	if e != nil {
		interval = max(interval, e.RetryAfter)
	}
	base := a.config.PollInterval
	if a.config.Mode == "live" {
		base = max(base, 3*time.Second)
	}
	if o != nil {
		copy := *o
		o = &copy
	}
	t.ScheduleOutcome = &mediaScheduleOutcome{ID: newID("schedule_observation"), At: time.Now().UTC(), Lane: l, ChannelKey: key, Observation: o, Temporary: temporary, Progress: result.Progress, BaseInterval: base, ServerInterval: interval}
}

// Positive deterministic jitter is computed from one immutable observation.
// Transaction retries and restart never sample a fresh shorter delay.
func mediaScheduleDelay(o *mediaScheduleOutcome, old mediaTaskSchedule) (time.Time, int, int) {
	fail, unchanged := old.FailureCount, old.UnchangedCount
	d := max(o.BaseInterval, o.ServerInterval)
	if o.Temporary {
		fail = min(30, fail+1)
		d = max(d, min(5*time.Minute, 3*time.Second*time.Duration(1<<min(7, fail-1))))
	} else {
		fail = 0
		if o.Lane == mediaQueryLane && o.Progress <= old.LastProgress {
			unchanged = min(30, unchanged+1)
			d = max(d, min(time.Minute, max(o.BaseInterval, 3*time.Second)*time.Duration(1<<min(5, unchanged/3))))
		} else {
			unchanged = 0
		}
	}
	if o.Temporary || unchanged > 0 {
		h := sha256.Sum256([]byte(o.ID))
		d += time.Duration(binary.BigEndian.Uint32(h[:4])%251) * d / 1000
	}
	due := o.At.Add(d)
	if o.Observation != nil && o.Observation.HasServerDelay && o.Observation.RetryNotBefore.After(due) {
		due = o.Observation.RetryNotBefore
	}
	return due, fail, unchanged
}
