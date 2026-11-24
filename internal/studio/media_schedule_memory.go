package studio

import (
	"sort"
	"time"
)

func memoryMediaSchedule(t *taskRecord) *mediaTaskSchedule {
	l := mediaLaneForTask(t)
	key, err := mediaFrozenChannel(t, l)
	if err != nil {
		return nil
	}
	if t.Schedule == nil {
		t.Schedule = &mediaTaskSchedule{TaskID: t.Task.ID, WorkspaceID: t.Task.WorkspaceID, ProviderMode: t.Task.ProviderMode, Version: 1, LastProgress: t.Task.Progress}
	}
	s := t.Schedule
	if s.Lane != "" && s.Lane != mediaLaneName(l) {
		s.FailureCount = 0
		s.UnchangedCount = 0
		s.LastProgress = t.Task.Progress
		s.ServerNotBefore = time.Time{}
	}
	s.Lane = mediaLaneName(l)
	s.ChannelKey = key
	s.EligibleAt = t.NextPoll
	s.Active = !terminal(t.Task.Status) && t.Task.Status != "SUBMIT_UNKNOWN" && t.Task.Status != "RECONCILE_REQUIRED"
	return s
}

// Called with App.mu. The memory demo's accepted cache is already bounded;
// sorted keys and persistent-in-process cursors mirror the PG ring semantics.
func (a *App) selectMemoryScheduleLocked(lane mediaExecutionLane, id string) (*taskRecord, *mediaScheduleCandidate) {
	s := a.mediaScheduler
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if id != "" {
		t := a.tasks[id]
		if t == nil {
			return nil, nil
		}
		q := memoryMediaSchedule(t)
		if q == nil || !q.Active || q.Lane != mediaLaneName(lane) || t.InFlight || a.mediaExecution.memoryAttempts[id] != "" || now.Before(q.EligibleAt) || now.Before(q.ServerNotBefore) {
			return nil, nil
		}
		r := s.memoryChannels[lane][q.ChannelKey]
		if r.Parked || now.Before(r.CooldownUntil) || r.ProbeID != "" && r.ProbeUntil != nil && now.Before(*r.ProbeUntil) {
			return nil, nil
		}
		return t, &mediaScheduleCandidate{TaskID: id, WorkspaceID: q.WorkspaceID, ProviderMode: q.ProviderMode, Lane: lane, ChannelKey: q.ChannelKey, ProjectionVersion: q.Version, ChannelVersion: r.Version, EffectiveLimit: r.EffectiveLimit, HardLimit: s.channelLimit(lane)}
	}
	groups := map[string][]string{}
	for taskID, t := range a.tasks {
		q := memoryMediaSchedule(t)
		if q != nil && q.Active && q.Lane == mediaLaneName(lane) {
			groups[t.Task.WorkspaceID] = append(groups[t.Task.WorkspaceID], taskID)
		}
	}
	spaces := make([]string, 0, len(groups))
	for w := range groups {
		spaces = append(spaces, w)
		sort.Strings(groups[w])
	}
	sort.Strings(spaces)
	start := sort.SearchStrings(spaces, s.workspaceCursor[lane])
	for start < len(spaces) && spaces[start] <= s.workspaceCursor[lane] {
		start++
	}
	for n := 0; n < min(len(spaces), mediaScheduleWorkspacePage); n++ {
		ws := spaces[(start+n)%len(spaces)]
		ids := groups[ws]
		cursor := s.taskCursor[lane][ws]
		offset := sort.SearchStrings(ids, cursor)
		for offset < len(ids) && ids[offset] <= cursor {
			offset++
		}
		for j := 0; j < min(len(ids), mediaScheduleTaskPage); j++ {
			taskID := ids[(offset+j)%len(ids)]
			t := a.tasks[taskID]
			s.taskCursor[lane][ws] = taskID
			s.workspaceCursor[lane] = ws
			if id != "" && id != taskID {
				continue
			}
			s.stats.Lanes[lane].ExaminedCandidates++
			if t.InFlight || a.mediaExecution.memoryAttempts[taskID] != "" || now.Before(t.NextPoll) || now.Before(t.Schedule.ServerNotBefore) {
				continue
			}
			r := s.memoryChannels[lane][t.Schedule.ChannelKey]
			if r.Parked || now.Before(r.CooldownUntil) || r.ProbeID != "" && r.ProbeUntil != nil && now.Before(*r.ProbeUntil) {
				s.stats.Lanes[lane].CooldownDeferrals++
				continue
			}
			return t, &mediaScheduleCandidate{TaskID: taskID, WorkspaceID: ws, ProviderMode: t.Task.ProviderMode, Lane: lane, ChannelKey: t.Schedule.ChannelKey, ProjectionVersion: t.Schedule.Version, ChannelVersion: r.Version, EffectiveLimit: r.EffectiveLimit, HardLimit: s.channelLimit(lane)}
		}
	}
	return nil, nil
}

func (a *App) applyMemoryMediaSchedule(t *taskRecord) {
	old := cloneMediaTaskSchedule(t.Schedule)
	q := memoryMediaSchedule(t)
	if q == nil {
		return
	}
	o := t.ScheduleOutcome
	if o != nil && q.LastObservationID != o.ID {
		if o.Observation != nil && o.Observation.HasServerDelay && (q.Lane == mediaLaneName(o.Lane) || o.Lane == mediaSubmitLane && q.Lane == "query") {
			q.ServerNotBefore = maxTime(q.ServerNotBefore, o.Observation.RetryNotBefore)
		}
		if old == nil {
			old = cloneMediaTaskSchedule(q)
		}
		if q.Active && q.Lane == mediaLaneName(o.Lane) {
			t.NextPoll, q.FailureCount, q.UnchangedCount = mediaScheduleDelay(o, *old)
			q.EligibleAt = t.NextPoll
			q.LastProgress = max(old.LastProgress, min(100, max(0, o.Progress)))
		}
		a.mediaScheduler.mu.Lock()
		key := o.ChannelKey
		r := a.mediaScheduler.memoryChannels[o.Lane][key]
		limit := a.mediaScheduler.channelLimit(o.Lane)
		r = applyMediaChannelObservation(r, o, t.ScheduleClaim, limit)
		if r.Version > 0 {
			a.mediaScheduler.memoryChannels[o.Lane][key] = r
		}
		a.mediaScheduler.mu.Unlock()
		q.LastObservationID = o.ID
	}
	q.Version++
	t.ScheduleOutcome = nil
}

func (a *App) checkMemoryReconcileSchedule(t *taskRecord) error {
	s := t.Schedule
	if s == nil {
		return nil
	}
	a.mediaScheduler.mu.Lock()
	r := a.mediaScheduler.memoryChannels[mediaLaneFromName(s.Lane)][s.ChannelKey]
	a.mediaScheduler.mu.Unlock()
	if r.Parked || time.Now().Before(s.ServerNotBefore) || time.Now().Before(r.CooldownUntil) {
		return failure(429, "MEDIA_PROVIDER_COOLDOWN", "原服务端等待尚未结束，未重新安排查询")
	}
	return nil
}

func (a *App) claimMemoryScheduleProbe(c *mediaScheduleCandidate) {
	s := a.mediaScheduler
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.memoryChannels[c.Lane][c.ChannelKey]
	if c.Lane == mediaQueryLane && r.ProbeRequired {
		c.ProbeID = newID("media_probe")
		r.ProbeID = c.ProbeID
		until := time.Now().Add(3 * time.Minute)
		r.ProbeUntil = &until
		r.Version++
		c.ChannelVersion = r.Version
		s.memoryChannels[c.Lane][c.ChannelKey] = r
	}
}

func (a *App) finishMemoryScheduleProbe(c *mediaScheduleCandidate) {
	if c == nil || c.ProbeID == "" {
		return
	}
	s := a.mediaScheduler
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.memoryChannels[c.Lane][c.ChannelKey]
	if r.ProbeID == c.ProbeID {
		r.ProbeID = ""
		r.ProbeUntil = nil
		r.Version++
		s.memoryChannels[c.Lane][c.ChannelKey] = r
	}
}
