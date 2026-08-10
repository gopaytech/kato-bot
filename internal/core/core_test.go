package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- fakes ---

type fakeKato struct {
	ucs      []UseCase
	contract Contract
	runRes   RunResult
	runErr   error
	listErr  error
	getErr   error
	runCalls int
}

func (f *fakeKato) ListUseCases(context.Context) ([]UseCase, error) { return f.ucs, f.listErr }
func (f *fakeKato) GetUseCase(context.Context, string) (Contract, error) {
	return f.contract, f.getErr
}
func (f *fakeKato) Run(context.Context, string, map[string]string) (RunResult, error) {
	f.runCalls++
	return f.runRes, f.runErr
}

// regWith builds a single-cluster registry named `name` backed by k.
func regWith(name string, k KatoClient) *Registry {
	r := NewRegistry()
	r.Add(Cluster{Name: name}, k)
	return r
}

type call struct {
	kind     string
	reply    Reply
	clusters []Cluster
	ucs      []UseCase
	groups   []Group
	group    Group
	c        Contract
	useCase  string
	inputs   map[string]string
	res      RunResult
	msg      string
	prefill  map[string]string
	formErr  string
}

type fakeRenderer struct{ calls []call }

func (r *fakeRenderer) RenderClusterPicker(_ context.Context, rep Reply, clusters []Cluster) error {
	r.calls = append(r.calls, call{kind: "clusterpicker", reply: rep, clusters: clusters})
	return nil
}
func (r *fakeRenderer) RenderPicker(_ context.Context, rep Reply, ucs []UseCase, groups []Group) error {
	r.calls = append(r.calls, call{kind: "picker", reply: rep, ucs: ucs, groups: groups})
	return nil
}
func (r *fakeRenderer) RenderGroupConfirm(_ context.Context, rep Reply, g Group) error {
	r.calls = append(r.calls, call{kind: "groupconfirm", reply: rep, group: g})
	return nil
}
func (r *fakeRenderer) RenderForm(_ context.Context, rep Reply, c Contract, prefill map[string]string, formErr string) error {
	r.calls = append(r.calls, call{kind: "form", reply: rep, c: c, prefill: prefill, formErr: formErr})
	return nil
}
func (r *fakeRenderer) RenderRunning(_ context.Context, rep Reply, uc string, in map[string]string) error {
	r.calls = append(r.calls, call{kind: "running", reply: rep, useCase: uc, inputs: in})
	return nil
}
func (r *fakeRenderer) RenderResult(_ context.Context, rep Reply, uc string, in map[string]string, res RunResult) error {
	r.calls = append(r.calls, call{kind: "result", reply: rep, useCase: uc, inputs: in, res: res})
	return nil
}
func (r *fakeRenderer) RenderError(_ context.Context, rep Reply, msg string) error {
	r.calls = append(r.calls, call{kind: "error", reply: rep, msg: msg})
	return nil
}

func (r *fakeRenderer) kinds() []string {
	var k []string
	for _, c := range r.calls {
		k = append(k, c.kind)
	}
	return k
}

// --- tests ---

func TestHandleListClusters(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Cluster{Name: "prod", Label: "Production"}, &fakeKato{})
	reg.Add(Cluster{Name: "staging"}, &fakeKato{})
	r := &fakeRenderer{}
	c := &Core{Clusters: reg, R: r}

	deferred, err := c.Handle(context.Background(), ListClusters{Reply: Reply{ChatID: "ch", InReplyTo: "m1"}})
	if err != nil || deferred != nil {
		t.Fatalf("deferred=non-nil err=%v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "clusterpicker" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if len(r.calls[0].clusters) != 2 || r.calls[0].clusters[0].Name != "prod" {
		t.Fatalf("clusterpicker call = %+v", r.calls[0])
	}
}

func TestHandlePickClusterListsUseCases(t *testing.T) {
	k := &fakeKato{ucs: []UseCase{{Name: "a"}, {Name: "b"}}}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(),
		PickCluster{Reply: Reply{ChatID: "ch", MessageID: "card1", Cluster: "prod"}})
	if err != nil || deferred != nil {
		t.Fatalf("deferred=non-nil err=%v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "picker" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if len(r.calls[0].ucs) != 2 || r.calls[0].reply.Cluster != "prod" {
		t.Fatalf("picker call = %+v", r.calls[0])
	}
}

func TestHandleUnknownClusterRendersError(t *testing.T) {
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", &fakeKato{}), R: r}

	_, err := c.Handle(context.Background(), PickCluster{Reply: Reply{Cluster: "ghost"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "error" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if !strings.Contains(r.calls[0].msg, "ghost") {
		t.Fatalf("error msg = %q", r.calls[0].msg)
	}
}

func TestHandlePickClusterKatoDown(t *testing.T) {
	k := &fakeKato{listErr: errors.New("connection refused")}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	_, err := c.Handle(context.Background(), PickCluster{Reply: Reply{Cluster: "prod"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "error" {
		t.Fatalf("calls = %v", r.kinds())
	}
}

func TestHandlePickUseCase(t *testing.T) {
	k := &fakeKato{contract: Contract{Name: "pod-crashloop", Inputs: []InputDecl{{Name: "ns", Required: true}}}}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(),
		PickUseCase{Reply: Reply{ChatID: "ch", MessageID: "card1", Cluster: "prod"}, Name: "pod-crashloop"})
	if err != nil || deferred != nil {
		t.Fatalf("deferred=non-nil err=%v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "form" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if r.calls[0].c.Name != "pod-crashloop" || r.calls[0].formErr != "" || r.calls[0].prefill != nil {
		t.Fatalf("form call = %+v", r.calls[0])
	}
}

func TestHandlePickUseCaseUnknownCluster(t *testing.T) {
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", &fakeKato{}), R: r}

	_, _ = c.Handle(context.Background(), PickUseCase{Reply: Reply{Cluster: "ghost"}, Name: "x"})
	if len(r.calls) != 1 || r.calls[0].kind != "error" {
		t.Fatalf("calls = %v", r.kinds())
	}
}

// TestHandleRoutesToSelectedCluster proves the registry routes to the SELECTED cluster's
// client, not just the first one: with prod+staging registered, picking "staging" lists
// staging's use cases and a run hits only staging's client.
func TestHandleRoutesToSelectedCluster(t *testing.T) {
	prod := &fakeKato{ucs: []UseCase{{Name: "prod-uc"}}}
	staging := &fakeKato{
		ucs:      []UseCase{{Name: "staging-uc"}},
		contract: Contract{Name: "staging-uc"},
		runRes:   RunResult{Phase: "Completed", Summary: "ok"},
	}
	reg := NewRegistry()
	reg.Add(Cluster{Name: "prod"}, prod)
	reg.Add(Cluster{Name: "staging"}, staging)
	r := &fakeRenderer{}
	c := &Core{Clusters: reg, R: r}

	// Picking "staging" must list staging's use cases, not prod's.
	if _, err := c.Handle(context.Background(), PickCluster{Reply: Reply{Cluster: "staging"}}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "picker" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if len(r.calls[0].ucs) != 1 || r.calls[0].ucs[0].Name != "staging-uc" {
		t.Fatalf("expected staging use cases, got %+v", r.calls[0].ucs)
	}

	// Submitting against "staging" must run staging's client only, never prod's.
	deferred, err := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "card1", Cluster: "staging"}, Name: "staging-uc", Inputs: map[string]string{},
	})
	if err != nil || deferred == nil {
		t.Fatalf("deferred=nil err=%v", err)
	}
	if err := deferred(context.Background()); err != nil {
		t.Fatalf("deferred err = %v", err)
	}
	if staging.runCalls != 1 || prod.runCalls != 0 {
		t.Fatalf("routing wrong: staging.runCalls=%d prod.runCalls=%d (want 1, 0)", staging.runCalls, prod.runCalls)
	}
}

// TestHandleEmptyClusterRendersError covers the empty-Reply.Cluster branch of
// unknownClusterMsg (a stale card whose value carries no cluster).
func TestHandleEmptyClusterRendersError(t *testing.T) {
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", &fakeKato{}), R: r}

	if _, err := c.Handle(context.Background(), PickUseCase{Reply: Reply{Cluster: ""}, Name: "x"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "error" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if !strings.Contains(r.calls[0].msg, "no cluster selected") {
		t.Fatalf("error msg = %q", r.calls[0].msg)
	}
}

func TestHandleSubmitFormMissingRequired(t *testing.T) {
	k := &fakeKato{contract: Contract{Name: "uc", Inputs: []InputDecl{
		{Name: "namespace", Required: true}, {Name: "pod", Required: true},
	}}}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "card1", Cluster: "prod"}, Name: "uc",
		Inputs: map[string]string{"namespace": "payments", "pod": "  "}, // pod blank
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if deferred != nil {
		t.Fatal("must NOT run when required inputs missing")
	}
	if k.runCalls != 0 {
		t.Fatalf("kato.Run called %d times", k.runCalls)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "form" {
		t.Fatalf("calls = %v", r.kinds())
	}
	if r.calls[0].formErr == "" || r.calls[0].prefill["namespace"] != "payments" {
		t.Fatalf("expected form error + prefill, got %+v", r.calls[0])
	}
}

func TestHandleSubmitFormRunsAndRenders(t *testing.T) {
	k := &fakeKato{
		contract: Contract{Name: "uc", Inputs: []InputDecl{{Name: "namespace", Required: true}}},
		runRes:   RunResult{Run: "uc-abc", Phase: "Completed", Summary: "ok"},
	}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "card1", Cluster: "prod"}, Name: "uc",
		Inputs: map[string]string{"namespace": "payments"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].kind != "running" {
		t.Fatalf("after Handle, calls = %v (want [running])", r.kinds())
	}
	if deferred == nil {
		t.Fatal("expected a deferred run thunk")
	}
	if err := deferred(context.Background()); err != nil {
		t.Fatalf("deferred err = %v", err)
	}
	if k.runCalls != 1 {
		t.Fatalf("kato.Run called %d times", k.runCalls)
	}
	if len(r.calls) != 2 || r.calls[1].kind != "result" {
		t.Fatalf("calls = %v (want [running result])", r.kinds())
	}
	if r.calls[1].res.Summary != "ok" || r.calls[1].res.Run != "uc-abc" {
		t.Fatalf("result = %+v", r.calls[1].res)
	}
}

func TestHandleSubmitFormRunErrorBecomesResult(t *testing.T) {
	k := &fakeKato{
		contract: Contract{Name: "uc", Inputs: []InputDecl{{Name: "x", Required: false}}},
		runErr:   errors.New("connection refused"),
	}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "c", Cluster: "prod"}, Name: "uc", Inputs: map[string]string{},
	})
	if err != nil || deferred == nil {
		t.Fatalf("deferred=nil err=%v", err)
	}
	if err := deferred(context.Background()); err != nil {
		t.Fatalf("deferred err = %v", err)
	}
	last := r.calls[len(r.calls)-1]
	if last.kind != "result" || last.res.Err == nil {
		t.Fatalf("expected result with Err set, got %+v", last)
	}
}

// fakeStatusErr implements HTTPStatusError for testing the friendly mapping.
type fakeStatusErr struct {
	status int
	detail string
}

func (e *fakeStatusErr) Error() string   { return "kato error" }
func (e *fakeStatusErr) HTTPStatus() int { return e.status }
func (e *fakeStatusErr) Detail() string  { return e.detail }

func TestFriendlyKatoError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"400", &fakeStatusErr{400, "input pod is required"}, "invalid inputs: input pod is required"},
		{"404", &fakeStatusErr{404, "x"}, "use case not found in the cluster"},
		{"422", &fakeStatusErr{422, "x"}, "this use case failed validation in the cluster"},
		{"429", &fakeStatusErr{429, "x"}, "kato is busy (too many concurrent runs) — try again shortly"},
		{"500", &fakeStatusErr{500, "x"}, "kato had an internal error — try again shortly"},
		{"transport", errors.New("connection refused"), "couldn't reach kato: connection refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := friendlyKatoError(tc.err); got != tc.want {
				t.Errorf("friendlyKatoError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleSubmitFormRun400GoesBackToForm(t *testing.T) {
	k := &fakeKato{
		contract: Contract{Name: "uc", Inputs: []InputDecl{{Name: "pod", Required: true}}},
		runErr:   &fakeStatusErr{400, "pod 'x' not found"},
	}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, err := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "card1", Cluster: "prod"}, Name: "uc", Inputs: map[string]string{"pod": "x"},
	})
	if err != nil || deferred == nil {
		t.Fatalf("deferred=nil err=%v", err)
	}
	if err := deferred(context.Background()); err != nil {
		t.Fatalf("deferred err = %v", err)
	}
	last := r.calls[len(r.calls)-1]
	if last.kind != "form" {
		t.Fatalf("expected form on 400, got calls = %v", r.kinds())
	}
	if last.prefill["pod"] != "x" || last.formErr == "" {
		t.Fatalf("form should be prefilled with an error, got %+v", last)
	}
}

func TestHandleSubmitFormRun429IsFriendlyResult(t *testing.T) {
	k := &fakeKato{
		contract: Contract{Name: "uc", Inputs: []InputDecl{{Name: "x", Required: false}}},
		runErr:   &fakeStatusErr{429, "too many concurrent runs"},
	}
	r := &fakeRenderer{}
	c := &Core{Clusters: regWith("prod", k), R: r}

	deferred, _ := c.Handle(context.Background(), SubmitForm{
		Reply: Reply{MessageID: "c", Cluster: "prod"}, Name: "uc", Inputs: map[string]string{},
	})
	if deferred == nil {
		t.Fatal("expected deferred")
	}
	if err := deferred(context.Background()); err != nil {
		t.Fatalf("deferred err = %v", err)
	}
	last := r.calls[len(r.calls)-1]
	if last.kind != "result" || last.res.Err == nil {
		t.Fatalf("expected result with Err, got %+v", last)
	}
	if msg := last.res.Err.Error(); strings.Contains(msg, "kato 429") || !strings.Contains(msg, "busy") {
		t.Fatalf("expected friendly busy message, got %q", msg)
	}
}
