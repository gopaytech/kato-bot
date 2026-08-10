// internal/core/grouprunner_test.go
package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type groupFakeKato struct {
	mu       sync.Mutex
	inFlight int32
	maxSeen  int32
	fail429  map[string]int // deployment -> remaining 429s before success
	verdict  map[string]bool
}

func (f *groupFakeKato) ListUseCases(ctx context.Context) ([]UseCase, error) { return nil, nil }
func (f *groupFakeKato) GetUseCase(ctx context.Context, name string) (Contract, error) {
	return Contract{}, nil
}
func (f *groupFakeKato) Run(ctx context.Context, name string, inputs map[string]string) (RunResult, error) {
	n := atomic.AddInt32(&f.inFlight, 1)
	for {
		old := atomic.LoadInt32(&f.maxSeen)
		if n <= old || atomic.CompareAndSwapInt32(&f.maxSeen, old, n) {
			break
		}
	}
	defer atomic.AddInt32(&f.inFlight, -1)
	time.Sleep(2 * time.Millisecond)
	dep := inputs["deployment"]
	f.mu.Lock()
	if left := f.fail429[dep]; left > 0 {
		f.fail429[dep] = left - 1
		f.mu.Unlock()
		return RunResult{}, &statusErr{code: 429}
	}
	h, ok := f.verdict[dep]
	f.mu.Unlock()
	res := RunResult{Run: "run-" + dep, Phase: "Succeeded"}
	if ok {
		res.Healthy = &h
	}
	return res, nil
}

type statusErr struct{ code int }

func (e *statusErr) Error() string   { return "status" }
func (e *statusErr) HTTPStatus() int { return e.code }
func (e *statusErr) Detail() string  { return "busy" }

type recReporter struct {
	started bool
	total   int
	results []ServiceResult
	summ    GroupSummary
}

func (r *recReporter) Start(ctx context.Context, g Group, d GroupDest, total int) error {
	r.started = true
	r.total = total
	return nil
}
func (r *recReporter) ServiceDone(ctx context.Context, g Group, sr ServiceResult) error {
	r.results = append(r.results, sr)
	return nil
}
func (r *recReporter) Finish(ctx context.Context, s GroupSummary) error { r.summ = s; return nil }

func TestGroupRunnerFanOut(t *testing.T) {
	fk := &groupFakeKato{
		fail429: map[string]int{"payment-api": 1}, // one 429 then success
		verdict: map[string]bool{"cart-api": true, "payment-api": false},
	}
	reg := NewRegistry()
	reg.Add(Cluster{Name: "prod-1"}, fk)

	g := Group{
		Name: "critical", Cluster: "prod-1", UseCase: "dt", Concurrency: 2,
		Targets: []map[string]string{
			{"deployment": "cart-api"},
			{"deployment": "payment-api"},
			{"deployment": "search-api"}, // no verdict -> unknown
		},
	}
	rep := &recReporter{}
	gr := &GroupRunner{Clusters: reg, MaxRetries: 3, Backoff: func(int) time.Duration { return time.Millisecond }}

	if err := gr.Run(context.Background(), g, GroupDest{}, rep); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.started || rep.total != 3 {
		t.Fatalf("Start not called correctly: started=%v total=%d", rep.started, rep.total)
	}
	if len(rep.results) != 3 {
		t.Fatalf("ServiceDone count = %d, want 3", len(rep.results))
	}
	if rep.summ.Healthy != 1 || rep.summ.Unhealthy != 1 || rep.summ.Unknown != 1 || rep.summ.Errored != 0 {
		t.Errorf("tallies = %+v, want H1 U1 Unk1 E0", rep.summ)
	}
	if fk.maxSeen > 2 {
		t.Errorf("max concurrency = %d, want <= 2", fk.maxSeen)
	}
}

// TestGroupRunnerTryAcquire proves TryAcquire is the single per-group in-flight
// gate: a second acquire of the same name fails while the first is held,
// succeeds again once released, and different group names are independent of
// each other.
func TestGroupRunnerTryAcquire(t *testing.T) {
	gr := &GroupRunner{}

	release, ok := gr.TryAcquire("critical")
	if !ok || release == nil {
		t.Fatalf("first TryAcquire(critical) ok=%v release==nil=%v, want ok=true with non-nil release", ok, release == nil)
	}

	if _, ok := gr.TryAcquire("critical"); ok {
		t.Fatal("second TryAcquire(critical) while first is held = ok, want !ok")
	}

	// A different group name is independent of "critical" being held.
	release2, ok := gr.TryAcquire("other")
	if !ok || release2 == nil {
		t.Fatalf("TryAcquire(other) ok=%v release==nil=%v, want ok=true (independent of critical)", ok, release2 == nil)
	}
	release2()

	release()
	release3, ok := gr.TryAcquire("critical")
	if !ok || release3 == nil {
		t.Fatalf("TryAcquire(critical) after release ok=%v release==nil=%v, want ok=true", ok, release3 == nil)
	}
	release3()
}

// blockingKato is a KatoClient whose Run signals (non-blocking, best-effort)
// that a call started, then blocks until ctx is cancelled and returns ctx.Err().
// Used to deterministically put GroupRunner.Run mid-flight before cancelling,
// without racing wall-clock sleeps.
type blockingKato struct {
	started chan struct{}
}

func (k *blockingKato) ListUseCases(ctx context.Context) ([]UseCase, error) { return nil, nil }
func (k *blockingKato) GetUseCase(ctx context.Context, name string) (Contract, error) {
	return Contract{}, nil
}
func (k *blockingKato) Run(ctx context.Context, name string, inputs map[string]string) (RunResult, error) {
	select {
	case k.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return RunResult{}, ctx.Err()
}

// finishSignalReporter wraps recReporter and closes `finished` once Finish is
// called, so the test can assert Finish ran without polling/sleeping.
type finishSignalReporter struct {
	recReporter
	finished chan struct{}
}

func (r *finishSignalReporter) Finish(ctx context.Context, s GroupSummary) error {
	err := r.recReporter.Finish(ctx, s)
	close(r.finished)
	return err
}

// TestGroupRunnerRunTerminatesOnCtxCancel proves Run terminates cleanly (does
// not hang) when its ctx is cancelled mid-run, and that the reporter still
// receives Finish with the in-flight targets bucketed as errored (ctx
// cancellation surfaces as their ServiceResult.Err).
func TestGroupRunnerRunTerminatesOnCtxCancel(t *testing.T) {
	const n = 3
	fk := &blockingKato{started: make(chan struct{}, n)}
	reg := NewRegistry()
	reg.Add(Cluster{Name: "prod-1"}, fk)

	targets := make([]map[string]string, n)
	for i := range targets {
		targets[i] = map[string]string{"deployment": "svc"}
	}
	g := Group{Name: "critical", Cluster: "prod-1", UseCase: "dt", Concurrency: n, Targets: targets}

	rep := &finishSignalReporter{finished: make(chan struct{})}
	gr := &GroupRunner{Clusters: reg, MaxRetries: 0}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- gr.Run(ctx, g, GroupDest{}, rep) }()

	// Wait until at least one worker is genuinely mid-run before cancelling, so
	// this exercises cancellation DURING a run, not before one ever starts.
	select {
	case <-fk.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a run to start")
	}
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancellation — hung")
	}

	select {
	case <-rep.finished:
	case <-time.After(time.Second):
		t.Fatal("reporter.Finish was never called")
	}

	if !rep.started || rep.total != n {
		t.Fatalf("Start not called correctly: started=%v total=%d, want true %d", rep.started, rep.total, n)
	}
	for _, r := range rep.results {
		if r.Bucket() != "errored" {
			t.Errorf("result %+v bucket = %q, want errored (ctx cancellation)", r, r.Bucket())
		}
	}
}
