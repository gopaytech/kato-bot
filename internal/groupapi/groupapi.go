// Package groupapi is the non-interactive front door for predefined groups: it
// backs the REST kick-off endpoint and the MCP run_group/get_group_run/
// list_groups tools. A group run is submitted and executed in the
// background, decoupled from the request that triggered it; the caller polls
// for the JSON result by runId. This is the async twin of the Lark adapter's
// handleGroupRun and depends only on core + gateway (never lark), so both
// internal/api and internal/mcp can use it without importing the Lark
// platform package.
package groupapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
	"github.com/zufardhiyaulhaq/kato-bot/internal/gateway"
)

// Service submits predefined-group runs on behalf of the REST endpoint and
// MCP tools, running each in the background and holding its result in an
// in-memory store the caller polls by runId. It is the async twin of the
// Lark adapter's handleGroupRun.
//
// The store is ephemeral: in-process, single-replica, bounded (maxRuns,
// runTTL). A restart loses in-flight and recently finished runs, same
// documented limitation as today's async single run.
type Service struct {
	Groups  *core.GroupRegistry
	Runner  *core.GroupRunner
	Timeout time.Duration // bounds each run; <=0 falls back to defaultGroupTimeout

	mu   sync.Mutex
	runs map[string]*runRecord
}

// New builds a Service. timeout bounds each run started by Submit (mirrors
// the interactive Lark path's GroupTimeout); <=0 falls back to the same
// 30-minute default as the interactive path, so a misconfigured/zero timeout
// can never mean "no deadline".
func New(groups *core.GroupRegistry, runner *core.GroupRunner, timeout time.Duration) *Service {
	return &Service{Groups: groups, Runner: runner, Timeout: timeout, runs: map[string]*runRecord{}}
}

type groupView struct {
	Name    string `json:"name"`
	Cluster string `json:"cluster"`
	UseCase string `json:"usecase"`
	Targets int    `json:"targets"`
}

// ListJSON renders {"groups":[...]}.
func (s *Service) ListJSON() []byte {
	gs := s.Groups.List()
	views := make([]groupView, 0, len(gs))
	for _, g := range gs {
		views = append(views, groupView{Name: g.Name, Cluster: g.Cluster, UseCase: g.UseCase, Targets: len(g.Targets)})
	}
	b, _ := json.Marshal(map[string]any{"groups": views})
	return b
}

// defaultGroupTimeout is the fallback run budget applied when Service.Timeout
// is <=0 (misconfigured/zero), matching the interactive Lark path's fallback
// (dispatch.go handleGroupRun) so a group run can never be unbounded.
const defaultGroupTimeout = 30 * time.Minute

// maxRuns bounds the in-memory run store; runTTL bounds how long a finished
// (done/failed) run's record is kept before it's eligible for eviction. Both
// are enforced lazily on Submit — a running record is never evicted.
const (
	maxRuns = 256
	runTTL  = time.Hour
)

// Tallies summarizes a group run's per-service outcomes.
type Tallies struct {
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Errored   int `json:"errored"`
	Unknown   int `json:"unknown"`
	Total     int `json:"total"`
}

// ServiceView is one target's outcome within a group run's JSON result.
type ServiceView struct {
	Target   map[string]string `json:"target"`
	Healthy  *bool             `json:"healthy,omitempty"`
	Headline string            `json:"headline,omitempty"`
	Phase    string            `json:"phase,omitempty"`
	Run      string            `json:"run,omitempty"`
	Summary  string            `json:"summary,omitempty"`
	Warning  string            `json:"warning,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// GroupResult is a group run's full JSON result: the group's identity, final
// tallies, and every service's outcome. Delivered via RunView.Result once a
// submitted run reaches "done".
type GroupResult struct {
	Group    string        `json:"group"`
	Cluster  string        `json:"cluster"`
	UseCase  string        `json:"usecase"`
	Tallies  Tallies       `json:"tallies"`
	Services []ServiceView `json:"services"`
}

// runStatus is a run's lifecycle state as exposed over JSON.
type runStatus string

const (
	statusRunning runStatus = "running"
	statusDone    runStatus = "done"
	statusFailed  runStatus = "failed"
)

// runRecord is a submitted group run's mutable state, held in Service.runs
// under Service.mu. StartedAt/Group/Cluster/UseCase are set once at Submit
// and never change; Status/CompletedAt/Result/ErrMsg are set once, by the
// background goroutine, when the run reaches a terminal state.
type runRecord struct {
	ID          string
	Group       string
	Cluster     string
	UseCase     string
	Status      runStatus
	StartedAt   time.Time
	CompletedAt time.Time // zero while running
	Result      *GroupResult
	ErrMsg      string
}

// RunView is a run's JSON view, returned by GetRun.
type RunView struct {
	RunID       string       `json:"runId"`
	Group       string       `json:"group"`
	Cluster     string       `json:"cluster"`
	UseCase     string       `json:"usecase"`
	Status      string       `json:"status"` // running|done|failed
	StartedAt   string       `json:"startedAt"`
	CompletedAt string       `json:"completedAt,omitempty"`
	Result      *GroupResult `json:"result,omitempty"`
	Error       string       `json:"error,omitempty"`
}

// Submit validates and launches a group run in the background, returning a
// runId to poll via GetRun. It does not block on the run finishing — the run
// executes on its own goroutine bound to context.Background() (not any
// request context), so a client disconnect can never abort it. 404 for an
// unknown group; 409 if a run for this group (from either this path or the
// interactive Lark path) is already in flight — both entry points share one
// gate via core.GroupRunner.TryAcquire.
func (s *Service) Submit(name string) (string, *gateway.Error) {
	g, ok := s.Groups.Get(name)
	if !ok {
		return "", &gateway.Error{Status: http.StatusNotFound, Msg: "unknown group " + name}
	}
	release, ok := s.Runner.TryAcquire(name)
	if !ok {
		return "", &gateway.Error{Status: http.StatusConflict, Msg: "group " + name + " is already running"}
	}

	runID := newRunID()
	rec := &runRecord{
		ID:        runID,
		Group:     g.Name,
		Cluster:   g.Cluster,
		UseCase:   g.UseCase,
		Status:    statusRunning,
		StartedAt: time.Now(),
	}

	s.mu.Lock()
	if s.runs == nil {
		s.runs = map[string]*runRecord{}
	}
	s.evictLocked()
	s.runs[runID] = rec
	s.mu.Unlock()

	go func() {
		defer release()

		to := s.Timeout
		if to <= 0 {
			to = defaultGroupTimeout
		}
		// context.Background(), not the submitting request's ctx: the run must
		// outlive the HTTP/MCP call that kicked it off, bounded only by to.
		ctx, cancel := context.WithTimeout(context.Background(), to)
		defer cancel()

		rep := &core.CollectingReporter{}
		err := s.Runner.Run(ctx, g, core.GroupDest{}, rep)

		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			rec.Status = statusFailed
			rec.ErrMsg = err.Error()
		} else {
			rec.Status = statusDone
			rec.Result = buildGroupResult(g, rep)
		}
		rec.CompletedAt = time.Now()
	}()

	return runID, nil
}

// GetRun returns a view of a run by id. 404 if unknown or evicted.
func (s *Service) GetRun(runID string) (*RunView, *gateway.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.runs[runID]
	if !ok {
		return nil, &gateway.Error{Status: http.StatusNotFound, Msg: "unknown run " + runID}
	}
	return viewOf(rec), nil
}

// viewOf renders a runRecord into its JSON view. Callers must hold s.mu.
func viewOf(r *runRecord) *RunView {
	v := &RunView{
		RunID:     r.ID,
		Group:     r.Group,
		Cluster:   r.Cluster,
		UseCase:   r.UseCase,
		Status:    string(r.Status),
		StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
		Result:    r.Result,
		Error:     r.ErrMsg,
	}
	if !r.CompletedAt.IsZero() {
		v.CompletedAt = r.CompletedAt.UTC().Format(time.RFC3339)
	}
	return v
}

// evictLocked bounds the run store: it first drops terminal (done/failed)
// records older than runTTL (by CompletedAt), then — if the store is still
// at or over maxRuns — evicts the oldest remaining terminal record (by
// StartedAt) repeatedly until under the cap. A running record is never
// evicted. Callers must hold s.mu.
func (s *Service) evictLocked() {
	now := time.Now()
	for id, r := range s.runs {
		if r.Status != statusRunning && !r.CompletedAt.IsZero() && now.Sub(r.CompletedAt) > runTTL {
			delete(s.runs, id)
		}
	}
	for len(s.runs) >= maxRuns {
		oldestID := ""
		var oldestStart time.Time
		found := false
		for id, r := range s.runs {
			if r.Status == statusRunning {
				continue
			}
			if !found || r.StartedAt.Before(oldestStart) {
				oldestID, oldestStart, found = id, r.StartedAt, true
			}
		}
		if !found {
			// Nothing terminal left to evict (store is all running records);
			// stop rather than evict a running one.
			break
		}
		delete(s.runs, oldestID)
	}
}

// newRunID generates a 16-byte hex run id.
func newRunID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read failing is effectively unheard of on supported
		// platforms; fall back to a timestamp-based id rather than panicking.
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// buildGroupResult maps a CollectingReporter's accumulated results/summary
// into the JSON result shape.
func buildGroupResult(g core.Group, rep *core.CollectingReporter) *GroupResult {
	services := make([]ServiceView, 0, len(rep.Results))
	for _, r := range rep.Results {
		sv := ServiceView{
			Target:   r.Target,
			Healthy:  r.Healthy,
			Headline: r.Headline,
			Phase:    r.Phase,
			Run:      r.Run,
			Summary:  r.Summary,
			Warning:  r.Warning,
		}
		if r.Err != nil {
			sv.Error = r.Err.Error()
		}
		services = append(services, sv)
	}
	return &GroupResult{
		Group:   g.Name,
		Cluster: g.Cluster,
		UseCase: g.UseCase,
		Tallies: Tallies{
			Healthy:   rep.Summary.Healthy,
			Unhealthy: rep.Summary.Unhealthy,
			Errored:   rep.Summary.Errored,
			Unknown:   rep.Summary.Unknown,
			Total:     rep.Summary.Total,
		},
		Services: services,
	}
}
