// internal/core/group.go
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Group is a predefined batch: run UseCase across Targets in Cluster.
type Group struct {
	Name        string
	Cluster     string
	UseCase     string
	Concurrency int
	Targets     []map[string]string
}

// GroupRegistry resolves groups by name, insertion-ordered, read-only after startup.
type GroupRegistry struct {
	order  []Group
	byName map[string]Group
}

func NewGroupRegistry() *GroupRegistry { return &GroupRegistry{byName: map[string]Group{}} }

func (r *GroupRegistry) Add(g Group) {
	if _, exists := r.byName[g.Name]; !exists {
		r.order = append(r.order, g)
	}
	r.byName[g.Name] = g
}

func (r *GroupRegistry) List() []Group {
	out := make([]Group, len(r.order))
	copy(out, r.order)
	return out
}

func (r *GroupRegistry) Get(name string) (Group, bool) { g, ok := r.byName[name]; return g, ok }

func (r *GroupRegistry) ForCluster(cluster string) []Group {
	var out []Group
	for _, g := range r.order {
		if g.Cluster == cluster {
			out = append(out, g)
		}
	}
	return out
}

// ServiceResult is one target's outcome within a group run.
type ServiceResult struct {
	Index    int
	Target   map[string]string
	Run      string
	Phase    string
	Summary  string
	Warning  string
	Healthy  *bool
	Headline string
	Err      error
}

// Bucket classifies the result: errored (check failed to run) beats verdict.
func (s ServiceResult) Bucket() string {
	switch {
	case s.Err != nil:
		return "errored"
	case s.Healthy == nil:
		return "unknown"
	case *s.Healthy:
		return "healthy"
	default:
		return "unhealthy"
	}
}

// GroupSummary holds final tallies.
type GroupSummary struct {
	Group     Group
	Total     int
	Healthy   int
	Unhealthy int
	Errored   int
	Unknown   int
}

// GroupDest tells the reporter where to anchor the parent card: reply to a
// message (interactive) when InReplyTo is set. The non-interactive (JSON)
// caller passes a zero GroupDest — it doesn't post to Lark at all.
type GroupDest struct {
	InReplyTo string
}

// GroupReporter receives progress for one group run. GroupRunner calls these
// serially from one goroutine, so implementations need no internal locking.
type GroupReporter interface {
	Start(ctx context.Context, g Group, dest GroupDest, total int) error
	ServiceDone(ctx context.Context, g Group, r ServiceResult) error
	Finish(ctx context.Context, s GroupSummary) error
}

// CollectingReporter is a GroupReporter that accumulates per-service results and
// tallies for a non-interactive (JSON) caller instead of rendering anything.
type CollectingReporter struct {
	Results []ServiceResult
	Summary GroupSummary
}

func (c *CollectingReporter) Start(ctx context.Context, g Group, dest GroupDest, total int) error {
	c.Summary = GroupSummary{Group: g, Total: total}
	return nil
}

func (c *CollectingReporter) ServiceDone(ctx context.Context, g Group, r ServiceResult) error {
	c.Results = append(c.Results, r)
	switch r.Bucket() {
	case "healthy":
		c.Summary.Healthy++
	case "unhealthy":
		c.Summary.Unhealthy++
	case "errored":
		c.Summary.Errored++
	default:
		c.Summary.Unknown++
	}
	return nil
}

func (c *CollectingReporter) Finish(ctx context.Context, s GroupSummary) error {
	c.Summary = s
	return nil
}

// defaultGroupConcurrency is the worker-pool size used when a Group doesn't
// specify Concurrency (or specifies a non-positive value).
const defaultGroupConcurrency = 5

const defaultMaxGroupRetries = 3

// GroupRunner fans a group out to its cluster's KatoClient with a bounded worker
// pool, retrying kato 429/5xx, and drives a GroupReporter serially.
type GroupRunner struct {
	Clusters   *Registry
	MaxRetries int
	Backoff    func(attempt int) time.Duration

	mu       sync.Mutex
	inFlight map[string]bool // per-group name, gates concurrent runs of the same group
}

// TryAcquire reserves an exclusive in-flight slot for group `name`. It returns a
// release func and true when the slot was free; nil and false when the group is
// already running. Callers MUST call release() when the run finishes.
//
// This is the single source of truth for the per-group in-flight gate, shared by
// both the interactive Lark adapter and the REST/MCP groupapi.Service, so
// repeated triggers of the same group (from either entry point, or a mix of
// both) can't spawn overlapping fan-outs.
func (gr *GroupRunner) TryAcquire(name string) (release func(), ok bool) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	if gr.inFlight == nil {
		gr.inFlight = map[string]bool{}
	}
	if gr.inFlight[name] {
		return nil, false
	}
	gr.inFlight[name] = true
	return func() {
		gr.mu.Lock()
		delete(gr.inFlight, name)
		gr.mu.Unlock()
	}, true
}

func (gr *GroupRunner) backoff(attempt int) time.Duration {
	if gr.Backoff != nil {
		return gr.Backoff(attempt)
	}
	return time.Duration(attempt) * time.Second
}

func (gr *GroupRunner) Run(ctx context.Context, g Group, dest GroupDest, reporter GroupReporter) error {
	kc, ok := gr.Clusters.Get(g.Cluster)
	if !ok {
		return fmt.Errorf("group %q: unknown cluster %q", g.Name, g.Cluster)
	}
	total := len(g.Targets)
	if err := reporter.Start(ctx, g, dest, total); err != nil {
		return fmt.Errorf("group %q: start: %w", g.Name, err)
	}

	conc := g.Concurrency
	if conc < 1 {
		conc = defaultGroupConcurrency
	}
	maxRetries := gr.MaxRetries
	if maxRetries < 0 {
		maxRetries = defaultMaxGroupRetries
	}

	type job struct {
		idx    int
		target map[string]string
	}
	jobs := make(chan job)
	results := make(chan ServiceResult)

	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- gr.runOne(ctx, kc, g.UseCase, maxRetries, j.idx, j.target)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i, t := range g.Targets {
			select {
			case jobs <- job{idx: i, target: t}:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()

	summ := GroupSummary{Group: g, Total: total}
	for r := range results {
		switch r.Bucket() {
		case "healthy":
			summ.Healthy++
		case "unhealthy":
			summ.Unhealthy++
		case "errored":
			summ.Errored++
		default:
			summ.Unknown++
		}
		// A reporter error never aborts the group.
		_ = reporter.ServiceDone(ctx, g, r)
	}
	return reporter.Finish(ctx, summ)
}

func (gr *GroupRunner) runOne(ctx context.Context, kc KatoClient, usecase string, maxRetries, idx int, target map[string]string) ServiceResult {
	sr := ServiceResult{Index: idx, Target: target}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(gr.backoff(attempt)):
			case <-ctx.Done():
				sr.Err = ctx.Err()
				return sr
			}
		}
		res, err := kc.Run(ctx, usecase, target)
		if err == nil {
			sr.Run, sr.Phase, sr.Summary, sr.Warning = res.Run, res.Phase, res.Summary, res.Warning
			sr.Healthy, sr.Headline = res.Healthy, res.Headline
			return sr
		}
		lastErr = err
		if !retriable(err) {
			break
		}
	}
	sr.Err = lastErr
	return sr
}

// retriable: kato 429/5xx and transport errors are retriable; 4xx (bad request) is not.
func retriable(err error) bool {
	var se HTTPStatusError
	if errors.As(err, &se) {
		s := se.HTTPStatus()
		return s == 429 || s >= 500
	}
	return true
}
