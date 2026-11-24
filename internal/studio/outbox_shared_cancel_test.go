package studio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func waitFeedState(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("shared feed did not reach the expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSharedFeedCancelledFirstAndLastReader(t *testing.T) {
	for _, frame := range []bool{false, true} {
		name := "page"
		if frame {
			name = "task frame"
		}
		t.Run(name, func(t *testing.T) {
			var n outboxNotifications
			f, _, release := n.subscribe(subscriptionScope{"work", "proj"})
			defer release()
			type result struct {
				value uint64
				err   error
			}
			start := func(ctx context.Context, load func(context.Context) (uint64, error)) <-chan result {
				done := make(chan result, 1)
				go func() {
					if frame {
						b, err := f.taskFrame(ctx, "task", func(c context.Context, _ subscriptionScope, _ string) ([]byte, error) {
							v, e := load(c)
							return []byte{byte(v)}, e
						})
						var v uint64
						if len(b) == 1 {
							v = uint64(b[0])
						}
						done <- result{v, err}
					} else {
						page, _, err := f.page(ctx, 12, func(c context.Context, _ subscriptionScope, _ uint64) (durablePage, error) {
							v, e := load(c)
							return durablePage{High: v}, e
						})
						done <- result{page.High, err}
					}
				}()
				return done
			}
			var calls atomic.Int32
			working := make(chan context.Context, 1)
			finish := make(chan struct{})
			var finishOnce sync.Once
			unblock := func() { finishOnce.Do(func() { close(finish) }) }
			defer unblock()
			load := func(ctx context.Context) (uint64, error) {
				calls.Add(1)
				working <- ctx
				select {
				case <-finish:
					return 7, nil
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
			firstCtx, leaveFirst := context.WithCancel(t.Context())
			defer leaveFirst()
			first := start(firstCtx, load)
			sqlCtx := <-working
			peer := start(t.Context(), load)
			waitFeedState(t, func() bool {
				n.mu.Lock()
				defer n.mu.Unlock()
				job := f.pages[12]
				if frame {
					job = f.tasks["task"]
				}
				return job != nil && job.readers == 2
			})
			leaveFirst()
			select {
			case r := <-first:
				if !errors.Is(r.err, context.Canceled) {
					t.Fatal("cancelled handler read a result")
				}
			case <-time.After(time.Second):
				t.Fatal("first reader waited for another reader's database load")
			}
			if sqlCtx.Err() != nil || calls.Load() != 1 {
				t.Fatal("first disconnect stopped the live peer's read")
			}
			unblock()
			if r := <-peer; r.err != nil || r.value != 7 {
				t.Fatal("live peer lost the read")
			}
			n.projected(map[subscriptionScope]bool{f.scope: true})
			lastCtx, leaveLast := context.WithCancel(t.Context())
			defer leaveLast()
			stopped := make(chan struct{})
			cleanup := make(chan struct{})
			defer close(cleanup)
			last := start(lastCtx, func(ctx context.Context) (uint64, error) {
				working <- ctx
				<-ctx.Done()
				close(stopped)
				<-cleanup
				return 0, ctx.Err()
			})
			<-working
			leaveLast()
			select {
			case r := <-last:
				if !errors.Is(r.err, context.Canceled) {
					t.Fatal("last reader received a stale result")
				}
			case <-time.After(time.Second):
				t.Fatal("last handler waited for SQL cleanup")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("abandoned job continued running")
			}
			fresh := start(t.Context(), func(context.Context) (uint64, error) { return 9, nil })
			if r := <-fresh; r.err != nil || r.value != 9 {
				t.Fatal("new stream joined an abandoned read")
			}
		})
	}
}

func TestSharedFeedBoundsUnfinishedLoads(t *testing.T) {
	var n outboxNotifications
	f, _, release := n.subscribe(subscriptionScope{"w", "p"})
	defer release()
	started := make(chan struct{}, feedLoadLimit)
	workCtx, cancelWorkers := context.WithCancel(t.Context())
	defer cancelWorkers()
	var wg sync.WaitGroup
	for i := range feedLoadLimit {
		wg.Go(func() {
			f.page(workCtx, uint64(i), func(c context.Context, _ subscriptionScope, _ uint64) (durablePage, error) {
				started <- struct{}{}
				<-c.Done()
				return durablePage{}, c.Err()
			})
		})
	}
	for range feedLoadLimit {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("loads did not fill budget")
		}
	}
	expire, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := f.taskFrame(expire, "unstarted", func(context.Context, subscriptionScope, string) ([]byte, error) {
		t.Error("new SQL ran beyond the common page/frame budget")
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("queued caller did not observe cancellation", err)
	}
	cancelWorkers()
	wg.Wait()
	waitFeedState(t, func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.active == 0 && len(f.tasks) == 0 && len(f.pages) == 0
	})
}

func TestPostgresSharedFeedLogoutWhileReadSlotsBusy(t *testing.T) {
	f := newWorkflowIntegrationFixture(t)
	f.cfg.Admission.GlobalStreams = 192
	f.cfg.RunReplayReadConnections = 1
	f.cfg.Heartbeat = 50 * time.Millisecond
	a, s := f.open(t)
	c := authClient(t)
	owner := registerFixture(t, c, s.URL, "feed-revoke@example.test")
	scope := subscriptionScope{owner.Workspace.ID, owner.Project.ID}
	if a.store.feedReads == nil || cap(a.store.feedReads.slots) != feedReadConnections || a.store.streamAuth == nil || a.store.feedReads == a.store.streamAuth {
		t.Fatal("project reads and session validation must have separate bounded connections")
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var jobs sync.WaitGroup
	entered := make(chan struct{}, feedReadConnections)
	for range feedReadConnections {
		jobs.Go(func() {
			err := a.store.feedReads.borrow(t.Context(), func(ctx context.Context, conn *pgx.Conn) error {
				var readOnly bool
				if err := conn.QueryRow(ctx, "SELECT current_setting('default_transaction_read_only')='on'").Scan(&readOnly); err != nil {
					return err
				}
				if !readOnly {
					t.Error("feed connection allowed writes")
				}
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err != nil {
				t.Error("hold read-only connection", err)
			}
		})
	}
	defer func() { unblock(); jobs.Wait() }()
	for range feedReadConnections {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("feed connections were unavailable")
		}
	}
	path := s.URL + "/api/v1/workspaces/" + url.PathEscape(owner.Workspace.ID) + "/events?project_id=" + url.QueryEscape(owner.Project.ID)
	req, err := http.NewRequestWithContext(t.Context(), "GET", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		size int
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		resp, err := c.Do(req)
		if err != nil {
			finished <- outcome{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		finished <- outcome{len(body), err}
	}()
	waitFeedState(t, func() bool {
		n := &a.store.notifications
		n.mu.Lock()
		defer n.mu.Unlock()
		f := n.scopes[scope]
		return f != nil && f.pages[0] != nil && f.pages[0].readers == 1
	})
	status, _, _ := authRequest(t, c, s.URL, "POST", "/api/v1/auth/logout", "", "", nil)
	if status != 200 {
		t.Fatal("logout failed", status)
	}
	select {
	case r := <-finished:
		if r.err != nil || r.size != 0 {
			t.Fatal("revoked waiting subscriber received a payload or did not close cleanly")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked subscriber kept its HTTP slot while read connections remained busy")
	}
	waitFeedState(t, func() bool {
		n := &a.store.notifications
		n.mu.Lock()
		defer n.mu.Unlock()
		return len(a.streamSlots) == 0 && n.active == 0 && n.scopes[scope] == nil
	})
	unblock()
	jobs.Wait()
}
