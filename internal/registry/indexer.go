package registry

import (
	"context"
	"errors"
	"sync"
	"time"
)

// IndexStatus describes the state of an Indexer.
type IndexStatus struct {
	// Building is set while repositories are being indexed.
	Building bool
	// Done and Total count the repositories of the current or last build.
	Done, Total int
	// Gen increases whenever the index changes.
	Gen uint64
	// Err is set when the last build could not be saved or was cancelled.
	Err error
}

// Indexer keeps the skill index in memory and (re)builds it in the
// background. Only one build runs at a time; partial results are visible
// through Snapshot while it runs.
type Indexer struct {
	Fetcher Fetcher
	// Path is where the index is cached.
	Path string
	// Repos returns the repositories to index.
	Repos func() []Repo
	// TTL is how long a cached index is fresh. Stale indexes are still
	// served while they are rebuilt.
	TTL time.Duration

	mu  sync.Mutex
	idx *Index // never mutated once published
	st  IndexStatus
	run *indexRun // the running build; nil when idle

	saveMu sync.Mutex // serialises writes to Path
}

// indexRun is one call to Start that had work to do. Requests made while it
// runs are queued and handled before it completes, so done is only closed
// once every caller's request has been served.
type indexRun struct {
	done chan struct{}
	// queued collects requests made while the build was running.
	queued *indexRequest
	// result, valid once done is closed
	idx *Index
	err error
}

// indexRequest is what callers of Start asked for.
type indexRequest struct {
	// ctxs are the callers' contexts; the build runs with the first one
	// that is still live when it starts.
	ctxs    []context.Context
	refresh bool
	// repos is read on the caller's goroutine: Repos may look at state that
	// only the caller may access.
	repos    []Repo
	progress []Progress
}

// add merges a later request into q. The most recent repository list wins.
func (q *indexRequest) add(o *indexRequest) {
	q.refresh = q.refresh || o.refresh
	q.repos = o.repos
	q.progress = append(q.progress, o.progress...)
	q.ctxs = append(q.ctxs, o.ctxs...)
}

// ctx returns the earliest caller context that is still live, so a caller
// that went away after queueing does not cancel the build for the others.
func (q *indexRequest) ctx() context.Context {
	for _, c := range q.ctxs {
		if c.Err() == nil {
			return c
		}
	}
	return q.ctxs[0]
}

func (q *indexRequest) report(done, total int, r Repo, entries []Entry, err error) {
	for _, p := range q.progress {
		p(done, total, r, entries, err)
	}
}

// Start makes sure the index is loaded and up to date. It loads the cache,
// then indexes in the background:
//   - all repositories when refresh is set, the cache is missing or stale
//     (stale rebuilds reuse cached checkouts, refresh re-fetches them),
//   - otherwise only repositories missing from the cache.
//
// Repositories that are no longer returned by Repos are dropped. When a build
// is already running, the request is queued and served right after it, and
// the running build's channel is returned. The channel is closed when the
// index is complete. progress, if not nil, is called for each repository
// indexed for this request.
func (x *Indexer) Start(ctx context.Context, refresh bool, progress Progress) <-chan struct{} {
	return x.start(ctx, refresh, progress).done
}

func (x *Indexer) start(ctx context.Context, refresh bool, progress Progress) *indexRun {
	req := &indexRequest{ctxs: []context.Context{ctx}, refresh: refresh, repos: x.Repos()}
	if progress != nil {
		req.progress = []Progress{progress}
	}

	// Read the cache before taking the lock: decoding a large index must
	// not stall Snapshot.
	x.mu.Lock()
	loaded := x.idx != nil
	x.mu.Unlock()
	var cached *Index
	if !loaded {
		cached, _, _ = LoadIndexAny(x.Path, x.TTL)
	}

	x.mu.Lock()
	if r := x.run; r != nil {
		if r.queued == nil {
			r.queued = req
		} else {
			r.queued.add(req)
		}
		x.mu.Unlock()
		return r
	}
	before := x.idx
	if x.idx == nil {
		if cached != nil {
			x.idx = cached
		} else {
			x.idx = NewIndex()
			x.idx.BuiltAt = time.Time{} // never built: stale
		}
	}
	planned := x.idx
	todo, full := x.plan(req)
	if len(todo) == 0 {
		// Only signal a change when there is one, so pollers do not redo
		// their work for nothing.
		if x.idx != before || x.st.Err != nil {
			x.st = IndexStatus{Done: x.st.Done, Total: x.st.Total, Gen: x.st.Gen + 1}
		}
		r := &indexRun{done: make(chan struct{}), idx: x.idx}
		close(r.done)
		dropped := x.idx != planned
		x.mu.Unlock()
		if dropped {
			// Removed repositories were dropped and nothing needs a build
			// that would save it. The cache is best-effort.
			_ = x.save()
		}
		return r
	}
	r := &indexRun{done: make(chan struct{})}
	x.run = r
	x.begin(len(todo))
	x.mu.Unlock()
	go x.build(r, req, todo, full)
	return r
}

// plan drops removed repositories and returns the ones to index. full is set
// when all of them are indexed and the result is stamped as freshly built.
// x.mu must be held.
func (x *Indexer) plan(req *indexRequest) (todo []Repo, full bool) {
	x.idx = x.idx.Retain(req.repos)
	if req.refresh || x.TTL > 0 && time.Since(x.idx.BuiltAt) > x.TTL {
		return req.repos, true
	}
	return x.idx.Missing(req.repos), false
}

// begin resets the status for a build of n repositories. x.mu must be held.
func (x *Indexer) begin(n int) {
	x.st = IndexStatus{Building: true, Total: n, Gen: x.st.Gen + 1}
}

func (x *Indexer) build(r *indexRun, req *indexRequest, todo []Repo, full bool) {
	for {
		err := x.buildOnce(req, todo, full)

		x.mu.Lock()
		if r.queued != nil {
			// Serve requests made during this build: they may want a
			// re-fetch or know about repositories added since it began.
			req, r.queued = r.queued, nil
			planned := x.idx
			if todo, full = x.plan(req); len(todo) > 0 {
				x.begin(len(todo))
				x.mu.Unlock()
				continue
			}
			if x.idx != planned && err == nil {
				// Repositories were dropped after the build saved. x.run
				// is still set, so no other build starts meanwhile.
				x.mu.Unlock()
				err = x.save()
				x.mu.Lock()
			}
		}
		x.st.Building, x.st.Err = false, err
		x.st.Gen++
		r.idx, r.err = x.idx, err
		x.run = nil
		close(r.done)
		x.mu.Unlock()
		return
	}
}

// buildOnce indexes repos into x.idx and saves the result unless the
// request's context is cancelled.
func (x *Indexer) buildOnce(req *indexRequest, repos []Repo, full bool) error {
	ctx := req.ctx()
	started := time.Now().UTC()
	done := 0
	scanAll(ctx, x.Fetcher, repos, req.refresh, func(r Repo, entries []Entry, err error) {
		done++
		if ctx.Err() == nil {
			x.mu.Lock()
			x.idx = x.idx.Merge(r, entries, err)
			x.st.Done = done
			x.st.Gen++
			x.mu.Unlock()
		}
		req.report(done, len(repos), r, entries, err)
	})

	// A cancelled build keeps its partial results in memory but is never
	// stamped as built, so the next Start completes it.
	if err := ctx.Err(); err != nil {
		return err
	}
	if full {
		x.mu.Lock()
		idx := x.idx.Clone()
		idx.BuiltAt = started
		x.idx = idx
		x.mu.Unlock()
	}
	return x.save()
}

// save writes the current index to Path. Saves are serialised and each one
// writes the index published at that time, so an older index never replaces
// a newer one on disk. Published indexes are immutable, so the write itself
// needs no x.mu.
func (x *Indexer) save() error {
	x.saveMu.Lock()
	defer x.saveMu.Unlock()
	x.mu.Lock()
	idx := x.idx
	x.mu.Unlock()
	return idx.Save(x.Path)
}

// Snapshot returns the current index (nil before the first Start) and status.
// The returned index must not be modified.
func (x *Indexer) Snapshot() (*Index, IndexStatus) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.idx, x.st
}

// Wait starts the indexer if needed and blocks until the index is complete.
func (x *Indexer) Wait(ctx context.Context, refresh bool, progress Progress) (*Index, error) {
	for {
		r := x.start(ctx, refresh, progress)
		select {
		case <-r.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// The build may have been cancelled or timed out through another
		// caller's context; ours is still live, so start over.
		if isContextErr(r.err) && ctx.Err() == nil {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		return r.idx, nil
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
