package studio

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	feedPageCacheLimit  = 256
	feedLoadLimit       = 256
	feedReadConnections = 2
)

type outboxNotifications struct {
	mu     sync.Mutex
	scopes map[subscriptionScope]*projectFeed
	active int           // includes SQL, connection wait and cancelling loads until actual return
	space  chan struct{} // closed when a global load position becomes available
	recent list.List     // completed metadata pages only, across all active scopes
}

type projectFeed struct {
	owner    *outboxNotifications
	scope    subscriptionScope
	watchers map[chan struct{}]struct{}
	pages    map[uint64]*sharedFeedPage
	tasks    map[string]*sharedFeedPage // compatibility frames exist only during a read
}

type feedResult struct {
	page  durablePage
	frame []byte
}

type sharedFeedPage struct {
	feed     *projectFeed
	after    uint64
	done     chan struct{}
	result   feedResult
	taskID   string
	cancel   context.CancelFunc
	readers  int
	finished bool
	expires  time.Time
	err      error
	retry    bool
	node     *list.Element
}

func (n *outboxNotifications) subscribe(scope subscriptionScope) (*projectFeed, <-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.scopes == nil {
		n.scopes = make(map[subscriptionScope]*projectFeed)
	}
	f := n.scopes[scope]
	if f == nil {
		f = &projectFeed{owner: n, scope: scope, watchers: make(map[chan struct{}]struct{}), pages: make(map[uint64]*sharedFeedPage), tasks: make(map[string]*sharedFeedPage)}
		n.scopes[scope] = f
	}
	wake := make(chan struct{}, 1)
	f.watchers[wake] = struct{}{}
	return f, wake, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		delete(f.watchers, wake)
		if len(f.watchers) == 0 && n.scopes[scope] == f {
			f.invalidate()
			for _, task := range f.tasks {
				task.forget()
				task.cancel()
			}
			delete(n.scopes, scope)
		}
	}
}

// Invalidate under the same mutex as cache publication. A read that started
// before a checkpoint advance or retention trim cannot repopulate an old page.
func (f *projectFeed) invalidate() {
	for _, p := range f.pages {
		p.forget()
		if !p.finished {
			p.retry = true
			p.cancel()
		}
	}
}

func (p *sharedFeedPage) forget() {
	if p.taskID != "" {
		if p.feed.tasks[p.taskID] == p {
			delete(p.feed.tasks, p.taskID)
		}
	} else if p.feed.pages[p.after] == p {
		delete(p.feed.pages, p.after)
	}
	if p.node != nil {
		p.feed.owner.recent.Remove(p.node)
		p.node = nil
	}
}

func (n *outboxNotifications) projected(scopes map[subscriptionScope]bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for scope := range scopes {
		if f := n.scopes[scope]; f != nil {
			f.invalidate()
			for wake := range f.watchers {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}
}

// The shared load is owned by all its current readers. Cancelling the request
// that first needed the page must not strand it or cancel a peer's SQL.
func (f *projectFeed) page(ctx context.Context, after uint64, load func(context.Context, subscriptionScope, uint64) (durablePage, error)) (durablePage, time.Time, error) {
	result, expires, err := f.read(ctx, after, "", func(ctx context.Context) (feedResult, error) {
		page, err := load(ctx, f.scope, after)
		return feedResult{page: page}, err
	})
	return result.page, expires, err
}

func (f *projectFeed) taskFrame(ctx context.Context, id string, load func(context.Context, subscriptionScope, string) ([]byte, error)) ([]byte, error) {
	result, _, err := f.read(ctx, 0, id, func(ctx context.Context) (feedResult, error) {
		frame, err := load(ctx, f.scope, id)
		return feedResult{frame: frame}, err
	})
	return result.frame, err
}

func (f *projectFeed) read(parent context.Context, after uint64, taskID string, load func(context.Context) (feedResult, error)) (feedResult, time.Time, error) {
	ctx, cancel := context.WithTimeout(parent, runReplayPageTimeout)
	defer cancel()
	n := f.owner
	for {
		n.mu.Lock()
		if ctx.Err() != nil || len(f.watchers) == 0 {
			n.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return feedResult{}, time.Time{}, err
			}
			return feedResult{}, time.Time{}, context.Canceled
		}
		p := f.pages[after]
		if taskID != "" {
			p = f.tasks[taskID]
		}
		if p != nil && p.finished {
			if time.Now().Before(p.expires) {
				n.recent.MoveToFront(p.node)
				n.mu.Unlock()
				return p.result, p.expires, nil
			}
			p.forget()
			p = nil
		}
		if p == nil {
			if n.active >= feedLoadLimit {
				if n.space == nil {
					n.space = make(chan struct{})
				}
				space := n.space
				n.mu.Unlock()
				select {
				case <-space:
					continue
				case <-ctx.Done():
					return feedResult{}, time.Time{}, ctx.Err()
				}
			}
			loadCtx, stop := context.WithTimeout(context.Background(), runReplayPageTimeout)
			p = &sharedFeedPage{feed: f, after: after, taskID: taskID, done: make(chan struct{}), cancel: stop}
			if taskID == "" {
				f.pages[after] = p
			} else {
				f.tasks[taskID] = p
			}
			n.active++
			go p.run(loadCtx, load)
		}
		p.readers++
		n.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-p.done:
		}
		n.mu.Lock()
		p.readers--
		if p.readers == 0 && !p.finished {
			p.forget()
			p.cancel()
		}
		if err := ctx.Err(); err != nil {
			n.mu.Unlock()
			return feedResult{}, time.Time{}, err
		}
		if p.retry {
			n.mu.Unlock()
			continue
		}
		result, expires, err := p.result, p.expires, p.err
		n.mu.Unlock()
		return result, expires, err
	}
}

func (p *sharedFeedPage) run(ctx context.Context, load func(context.Context) (feedResult, error)) {
	result, err := load(ctx)
	n := p.feed.owner
	n.mu.Lock()
	defer n.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	p.cancel()
	p.result, p.err, p.finished = result, err, true
	if p.taskID != "" || err != nil {
		p.forget()
	} else if p.feed.pages[p.after] == p {
		p.expires = time.Now().Add(time.Second)
		p.node = n.recent.PushFront(p)
		if n.recent.Len() > feedPageCacheLimit {
			n.recent.Back().Value.(*sharedFeedPage).forget()
		}
	}
	n.active--
	close(p.done)
	if n.space != nil {
		close(n.space)
		n.space = nil
	}
}

func (s *postgresStore) readFeedTask(readCtx context.Context, scope subscriptionScope, id string) ([]byte, error) {
	var task Task
	err := s.withVerifiedSnapshot(readCtx, s.feedReads, func(ctx context.Context, conn pgx.Tx) error {
		var data []byte
		if err := conn.QueryRow(ctx, "SELECT document FROM studio_tasks WHERE id=$1 AND workspace_id=$2 AND project_id=$3", id, scope.WorkspaceID, scope.ProjectID).Scan(&data); err != nil {
			return err
		}
		return json.Unmarshal(data, &task)
	})
	if err != nil {
		return nil, err
	}
	if task.ID != id || task.WorkspaceID != scope.WorkspaceID || task.ProjectID != scope.ProjectID {
		return nil, failure(404, "NOT_FOUND", "任务不存在")
	}
	return json.Marshal(map[string]any{"task": copyTask(task)})
}
