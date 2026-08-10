package groupapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// fakeKato is a minimal core.KatoClient used to exercise Submit's full path
// through the GroupRunner into a CollectingReporter.
type fakeKato struct{}

func (fakeKato) ListUseCases(ctx context.Context) ([]core.UseCase, error) { return nil, nil }
func (fakeKato) GetUseCase(ctx context.Context, name string) (core.Contract, error) {
	return core.Contract{}, nil
}
func (fakeKato) Run(ctx context.Context, name string, inputs map[string]string) (core.RunResult, error) {
	tru := true
	return core.RunResult{Run: "run-1", Phase: "Succeeded", Healthy: &tru, Headline: "ok", Summary: "all good"}, nil
}

// ctxCapturingKato records the ctx it was called with, so tests can inspect
// whether the ctx handed down from groupapi.Service's background goroutine
// through GroupRunner carries a deadline (and is NOT derived from any
// request context, since Submit doesn't even take one).
type ctxCapturingKato struct {
	ctxCh chan context.Context
}

func (ctxCapturingKato) ListUseCases(ctx context.Context) ([]core.UseCase, error) { return nil, nil }
func (ctxCapturingKato) GetUseCase(ctx context.Context, name string) (core.Contract, error) {
	return core.Contract{}, nil
}
func (k ctxCapturingKato) Run(ctx context.Context, name string, inputs map[string]string) (core.RunResult, error) {
	k.ctxCh <- ctx
	return core.RunResult{Run: "run-1", Phase: "Succeeded"}, nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{
		{"deployment": "a"},
		{"deployment": "b"},
	}})

	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, fakeKato{})

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}

	return New(groups, runner, time.Minute)
}

// waitForStatus polls GetRun until it reports want or the timeout elapses —
// a bounded wait loop instead of a blind sleep, since the run finishes on
// its own background goroutine.
func waitForStatus(t *testing.T, svc *Service, runID, want string, timeout time.Duration) *RunView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		view, e := svc.GetRun(runID)
		if e != nil {
			t.Fatalf("GetRun(%s) = %v, want nil error", runID, e)
		}
		if view.Status == want {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("GetRun(%s) status = %s, want %s (timed out)", runID, view.Status, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestServiceSubmitUnknownGroup(t *testing.T) {
	svc := newTestService(t)
	runID, e := svc.Submit("nope")
	if runID != "" || e == nil || e.Status != http.StatusNotFound {
		t.Fatalf("Submit(nope) = %q, %v, want empty runId and 404", runID, e)
	}
}

func TestServiceGetRunUnknownID(t *testing.T) {
	svc := newTestService(t)
	view, e := svc.GetRun("no-such-run")
	if view != nil || e == nil || e.Status != http.StatusNotFound {
		t.Fatalf("GetRun(no-such-run) = %v, %v, want nil view and 404", view, e)
	}
}

// TestServiceSubmitIsAsyncThenDone proves Submit returns immediately (status
// "running" right away, before the run can possibly have finished) and the
// run completes on its own background goroutine: after it's unblocked,
// GetRun eventually reports "done" with the correct GroupResult.
func TestServiceSubmitIsAsyncThenDone(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{
		{"deployment": "a"},
		{"deployment": "b"},
	}})
	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, blockingKato{started: started, release: release})
	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, time.Minute)

	runID, e := svc.Submit("ok")
	if e != nil {
		t.Fatalf("Submit(ok) = %v, want nil error", e)
	}
	if runID == "" {
		t.Fatal("Submit(ok) returned empty runId")
	}

	// Assert async: immediately after Submit returns, the run is still
	// "running" — Submit did not block on the run finishing.
	view, e := svc.GetRun(runID)
	if e != nil {
		t.Fatalf("GetRun(%s) = %v, want nil error", runID, e)
	}
	if view.Status != string(statusRunning) {
		t.Fatalf("status immediately after Submit = %s, want running", view.Status)
	}
	if view.Result != nil {
		t.Errorf("result while running = %+v, want nil", view.Result)
	}

	// Wait until the run has genuinely reached KatoClient.Run for both
	// targets before unblocking, so we know the fan-out is really in flight.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("run never reached KatoClient.Run")
		}
	}
	close(release)

	done := waitForStatus(t, svc, runID, string(statusDone), time.Second)
	if done.Result == nil {
		t.Fatal("done view has nil Result")
	}
	if done.Result.Group != "ok" || done.Result.Cluster != "prod" || done.Result.UseCase != "dt" {
		t.Errorf("result header = %+v", done.Result)
	}
	if done.Result.Tallies.Total != 2 || done.Result.Tallies.Healthy != 0 {
		// blockingKato's Run returns a bare RunResult with no Healthy verdict.
		t.Errorf("tallies = %+v, want Total=2", done.Result.Tallies)
	}
	if len(done.Result.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(done.Result.Services))
	}
	if done.CompletedAt == "" {
		t.Error("done view has empty CompletedAt")
	}
	if done.StartedAt == "" {
		t.Error("view has empty StartedAt")
	}
}

// TestServiceSubmitReturnsResult proves a full submit+poll round trip through
// GroupRunner into a CollectingReporter yields correct tallies and
// per-service views for a fake KatoClient.
func TestServiceSubmitReturnsResult(t *testing.T) {
	svc := newTestService(t)
	runID, e := svc.Submit("ok")
	if e != nil {
		t.Fatalf("Submit(ok) = %v, want nil error", e)
	}

	res := waitForStatus(t, svc, runID, string(statusDone), time.Second)
	if res.Result == nil {
		t.Fatal("done view has nil Result")
	}
	if res.Group != "ok" || res.Cluster != "prod" || res.UseCase != "dt" {
		t.Errorf("view header = %+v", res)
	}
	if res.Result.Tallies.Total != 2 || res.Result.Tallies.Healthy != 2 || res.Result.Tallies.Unhealthy != 0 ||
		res.Result.Tallies.Errored != 0 || res.Result.Tallies.Unknown != 0 {
		t.Errorf("tallies = %+v, want Total=2 Healthy=2", res.Result.Tallies)
	}
	if len(res.Result.Services) != 2 {
		t.Fatalf("services = %d, want 2", len(res.Result.Services))
	}
	for _, sv := range res.Result.Services {
		if sv.Healthy == nil || !*sv.Healthy {
			t.Errorf("service %+v healthy, want true", sv)
		}
		if sv.Run != "run-1" || sv.Headline != "ok" || sv.Summary != "all good" {
			t.Errorf("service view = %+v, want run-1/ok/all good", sv)
		}
		if sv.Error != "" {
			t.Errorf("service %+v error, want empty", sv)
		}
	}
}

// warningKato succeeds but returns a RunResult with a Warning (kato couldn't
// produce a summary but step outputs are valid), so tests can prove the
// warning is carried through into ServiceView.
type warningKato struct{}

func (warningKato) ListUseCases(ctx context.Context) ([]core.UseCase, error) { return nil, nil }
func (warningKato) GetUseCase(ctx context.Context, name string) (core.Contract, error) {
	return core.Contract{}, nil
}
func (warningKato) Run(ctx context.Context, name string, inputs map[string]string) (core.RunResult, error) {
	tru := true
	return core.RunResult{Run: "run-1", Phase: "Succeeded", Healthy: &tru, Headline: "ok", Warning: "summary unavailable"}, nil
}

// TestServiceSubmitCarriesWarning proves a RunResult.Warning (kato couldn't
// produce a summary but step outputs are valid) is carried through into the
// polled JSON result's ServiceView.Warning, not dropped.
func TestServiceSubmitCarriesWarning(t *testing.T) {
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, warningKato{})

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, time.Minute)

	runID, e := svc.Submit("ok")
	if e != nil {
		t.Fatalf("Submit(ok) = %v, want nil error", e)
	}
	res := waitForStatus(t, svc, runID, string(statusDone), time.Second)
	if len(res.Result.Services) != 1 || res.Result.Services[0].Warning != "summary unavailable" {
		t.Errorf("services = %+v, want one with Warning=summary unavailable", res.Result.Services)
	}
}

// erroringKato always fails, so the polled result surfaces the errored
// bucket and each service's ServiceView.Error — the run itself still
// reaches "done" (GroupRunner.Run only errors run-level on unknown cluster).
type erroringKato struct{}

func (erroringKato) ListUseCases(ctx context.Context) ([]core.UseCase, error) { return nil, nil }
func (erroringKato) GetUseCase(ctx context.Context, name string) (core.Contract, error) {
	return core.Contract{}, nil
}
func (erroringKato) Run(ctx context.Context, name string, inputs map[string]string) (core.RunResult, error) {
	return core.RunResult{}, &core.RunError{Msg: "boom"}
}

func TestServiceSubmitErroredService(t *testing.T) {
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, erroringKato{})

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, time.Minute)

	runID, e := svc.Submit("ok")
	if e != nil {
		t.Fatalf("Submit(ok) = %v, want nil error", e)
	}
	res := waitForStatus(t, svc, runID, string(statusDone), time.Second)
	if res.Result.Tallies.Errored != 1 || res.Result.Tallies.Total != 1 {
		t.Errorf("tallies = %+v, want Errored=1 Total=1", res.Result.Tallies)
	}
	if len(res.Result.Services) != 1 || res.Result.Services[0].Error != "boom" {
		t.Errorf("services = %+v, want one with Error=boom", res.Result.Services)
	}
}

// TestServiceSubmitRunLevelFailure proves that when GroupRunner.Run itself
// fails (not a per-service error — here, the group's cluster is absent from
// the runner's cluster registry, which core.GroupRegistry.Add does not
// validate), the polled run reaches "failed" with ErrMsg set, not "done".
func TestServiceSubmitRunLevelFailure(t *testing.T) {
	groups := core.NewGroupRegistry()
	// "prod2" is never registered in registry below, so GroupRunner.Run's
	// gr.Clusters.Get(g.Cluster) fails before any per-service work happens.
	groups.Add(core.Group{Name: "bad-cluster", Cluster: "prod2", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	registry := core.NewRegistry()
	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, time.Minute)

	runID, e := svc.Submit("bad-cluster")
	if e != nil {
		t.Fatalf("Submit(bad-cluster) = %v, want nil error", e)
	}

	res := waitForStatus(t, svc, runID, string(statusFailed), time.Second)
	if res.Error == "" {
		t.Error("failed view has empty Error")
	}
	if res.Result != nil {
		t.Errorf("failed view Result = %+v, want nil", res.Result)
	}
	if res.CompletedAt == "" {
		t.Error("failed view has empty CompletedAt")
	}
}

// blockingKato is a KatoClient whose Run signals that it started (so the test
// knows the run has genuinely reached the cluster) and then blocks on a
// release channel until the test lets it finish. Used to deterministically
// hold a group "in flight" without racing wall-clock sleeps.
type blockingKato struct {
	started chan struct{}
	release chan struct{}
}

func (blockingKato) ListUseCases(ctx context.Context) ([]core.UseCase, error) { return nil, nil }
func (blockingKato) GetUseCase(ctx context.Context, name string) (core.Contract, error) {
	return core.Contract{}, nil
}
func (k blockingKato) Run(ctx context.Context, name string, inputs map[string]string) (core.RunResult, error) {
	k.started <- struct{}{}
	<-k.release
	return core.RunResult{Run: "run-1", Phase: "Succeeded"}, nil
}

// TestServiceSubmitConflictWhileInFlight proves the REST/MCP path shares the
// same per-group in-flight gate as the interactive Lark path
// (core.GroupRunner.TryAcquire): a second Submit() for a group that is
// already running must be rejected with 409, instead of starting a second
// overlapping fan-out.
func TestServiceSubmitConflictWhileInFlight(t *testing.T) {
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	fk := blockingKato{started: make(chan struct{}, 1), release: make(chan struct{})}
	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, fk)

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, time.Minute)

	firstID, e := svc.Submit("ok")
	if e != nil {
		t.Fatalf("first Submit(ok) = %v, want nil", e)
	}

	// Wait for the first run to genuinely reach KatoClient.Run — i.e. it's in
	// flight, not just launched — before asserting the gate rejects a second one.
	select {
	case <-fk.started:
	case <-time.After(time.Second):
		t.Fatal("first run never reached KatoClient.Run")
	}

	if runID, e := svc.Submit("ok"); e == nil || e.Status != http.StatusConflict {
		t.Fatalf("second Submit(ok) while in flight = %q, %v, want empty runId and 409", runID, e)
	}

	close(fk.release)
	waitForStatus(t, svc, firstID, string(statusDone), time.Second)
}

// TestServiceSubmitAppliesTimeout proves the background run honors
// Service.Timeout: the ctx handed down to the cluster's KatoClient (through
// GroupRunner) must carry a deadline when Timeout > 0.
func TestServiceSubmitAppliesTimeout(t *testing.T) {
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	ctxCh := make(chan context.Context, 1)
	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, ctxCapturingKato{ctxCh: ctxCh})

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, 50*time.Millisecond)

	if _, e := svc.Submit("ok"); e != nil {
		t.Fatalf("Submit(ok) = %v, want nil", e)
	}

	var runCtx context.Context
	select {
	case runCtx = <-ctxCh:
	case <-time.After(time.Second):
		t.Fatal("KatoClient.Run was never called")
	}

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("ctx passed to KatoClient.Run has no deadline, want one derived from Service.Timeout")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 50*time.Millisecond {
		t.Fatalf("deadline remaining = %v, want in (0, 50ms]", remaining)
	}
}

// TestServiceSubmitZeroTimeoutFallsBackTo30Min proves Timeout<=0 does NOT
// leave the ctx handed to KatoClient.Run unbounded: it falls back to the same
// 30-minute default as the interactive Lark path (dispatch.go
// handleGroupRun), so a misconfigured/zero timeout can't produce an
// unbounded run.
func TestServiceSubmitZeroTimeoutFallsBackTo30Min(t *testing.T) {
	groups := core.NewGroupRegistry()
	groups.Add(core.Group{Name: "ok", Cluster: "prod", UseCase: "dt", Targets: []map[string]string{{"deployment": "x"}}})

	ctxCh := make(chan context.Context, 1)
	registry := core.NewRegistry()
	registry.Add(core.Cluster{Name: "prod"}, ctxCapturingKato{ctxCh: ctxCh})

	runner := &core.GroupRunner{Clusters: registry, MaxRetries: 0}
	svc := New(groups, runner, 0)

	if _, e := svc.Submit("ok"); e != nil {
		t.Fatalf("Submit(ok) = %v, want nil", e)
	}

	var runCtx context.Context
	select {
	case runCtx = <-ctxCh:
	case <-time.After(time.Second):
		t.Fatal("KatoClient.Run was never called")
	}

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("ctx passed to KatoClient.Run has no deadline, want the 30-minute fallback when Service.Timeout <= 0")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > defaultGroupTimeout {
		t.Fatalf("deadline remaining = %v, want in (0, %v]", remaining, defaultGroupTimeout)
	}
}

// TestEvictionNeverDropsRunning proves evictLocked shrinks an over-cap store
// by dropping the oldest terminal records but never touches a running one,
// even when every terminal record is evicted.
func TestEvictionNeverDropsRunning(t *testing.T) {
	svc := &Service{runs: map[string]*runRecord{}}
	now := time.Now()
	for i := 0; i < maxRuns+10; i++ {
		id := fmt.Sprintf("term-%d", i)
		svc.runs[id] = &runRecord{
			ID: id, Status: statusDone,
			StartedAt:   now.Add(time.Duration(i) * time.Second),
			CompletedAt: now,
		}
	}
	svc.runs["running-1"] = &runRecord{ID: "running-1", Status: statusRunning, StartedAt: now}

	svc.mu.Lock()
	svc.evictLocked()
	svc.mu.Unlock()

	if len(svc.runs) >= maxRuns {
		t.Errorf("len(runs) = %d after eviction, want < %d", len(svc.runs), maxRuns)
	}
	if _, ok := svc.runs["running-1"]; !ok {
		t.Error("evictLocked evicted the running record")
	}
}

// TestEvictionDropsExpiredTerminalRecords proves evictLocked drops a
// terminal record older than runTTL even when the store is well under
// maxRuns, and still never touches a running record.
func TestEvictionDropsExpiredTerminalRecords(t *testing.T) {
	svc := &Service{runs: map[string]*runRecord{}}
	old := time.Now().Add(-2 * runTTL)
	svc.runs["old"] = &runRecord{ID: "old", Status: statusDone, StartedAt: old, CompletedAt: old}
	svc.runs["running"] = &runRecord{ID: "running", Status: statusRunning, StartedAt: time.Now()}

	svc.mu.Lock()
	svc.evictLocked()
	svc.mu.Unlock()

	if _, ok := svc.runs["old"]; ok {
		t.Error("evictLocked did not drop an expired terminal record")
	}
	if _, ok := svc.runs["running"]; !ok {
		t.Error("evictLocked dropped the running record")
	}
}
