# kato-bot Group Runs + Native Lark Reporting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run one kato UseCase across a predefined static list of single-cluster targets ("a Group"), fanned out with backpressure, and report the results into Lark natively — a parent progress card plus one threaded reply per service — triggered either from the existing Lark picker or a kick-off endpoint / MCP tool.

**Architecture:** Groups are defined in config (a `groups.yaml` file, like `clusters.yaml`). A platform-agnostic `core.GroupRunner` fans out to a cluster's `KatoClient.Run` with a bounded worker pool, retries kato 429/5xx, and drives a `core.GroupReporter` port (called serially from one goroutine, so the Lark implementation needs no locks). The Lark reporter posts a parent card and threads per-service replies under it, patching the parent's tallies as runs land. Two entry points share it: the Lark card flow (a Groups section on the existing usecase picker) and a `POST /api/v1/groups/{name}/run` endpoint + `run_group` MCP tool. The per-service health verdict comes from kato (see the kato plan) via new `RunResult.Healthy/Headline` fields.

**Tech Stack:** Go 1.25, `larksuite/oapi-sdk-go/v3`, `modelcontextprotocol/go-sdk`, `gopkg.in/yaml.v3`, net/http (Go 1.22 method-prefixed routes), `go test -race`.

## Global Constraints

- **Toolchain discrepancy — reconcile first:** `go.mod` says `go 1.25.0` but `.tool-versions` pins `golang 1.24.8`. A 1.24.8 toolchain will refuse to build a `go 1.25.0` module. Before writing code, bump `.tool-versions` to a 1.25.x Go (matching the kato repo's `1.25.2`) or confirm auto-toolchain fetch. All commands are bare `go ...` / `make ...` (no `env GOROOT=` in this repo).
- **`internal/core` stays platform-agnostic:** it MUST NOT import `larksuite`, `lark`, `callback`, or any adapter package. Group execution logic lives in `core`; Lark rendering implements `core` ports.
- **Reporter callbacks are serialized:** `GroupRunner` calls `GroupReporter.Start/ServiceDone/Finish` from a single goroutine. Implementations must not assume concurrency but also need no internal locking.
- **Group fan-out does NOT use the card `runSem`:** that semaphore (cap `MaxConcurrentRuns`, default 4) gives interactive submits a "busy" card; a 100-target group would starve it. Group concurrency is its own per-group worker pool; a per-group in-flight guard prevents duplicate concurrent runs of the same group.
- **Statelessness preserved:** a group run is ephemeral in-memory state. kato-bot stays single-replica, no DB. A restart mid-group loses that run (re-trigger). Per-service audit lives in kato as a `Run` CR.
- **Single-run flow untouched:** the pick-cluster → pick-usecase → form → run path keeps identical behavior and adds no taps. Groups are additive.
- **Branching:** work on a dedicated branch (e.g. `feat/group-runs`); do not commit to `main`. Another agent may share this tree — coordinate or use a git worktree.

---

## File Structure

- `internal/config/config.go` (**modify**) — `GroupConfig` type, `Config.Groups`, `loadGroups()`, `KATO_GROUPS_FILE`, `GroupRunTimeout`.
- `internal/core/group.go` (**create**) — `Group`, `GroupRegistry`, `ServiceResult`, `GroupSummary`, `GroupDest`, `GroupReporter`, `GroupRunner`.
- `internal/core/types.go` (**modify**) — `RunResult` gains `Healthy *bool` + `Headline`; new intents `PickGroup`/`RunGroup`; `Renderer` gains `RenderPicker(...groups)` change + `RenderGroupConfirm`; `Core` gains `Groups`.
- `internal/core/core.go` (**modify**) — `PickCluster` passes groups to the picker; new `PickGroup` case.
- `internal/kato/client.go` (**modify**) — `Run` unmarshals `healthy`/`headline` into `RunResult`.
- `internal/platform/lark/groupcards.go` (**create**) — parent card + service-reply card builders + Groups section on the picker.
- `internal/platform/lark/cards.go` (**modify**) — `buildPickerCard` gains a Groups section; add `buildGroupConfirmCard`, `buildGroupStartedCard`.
- `internal/platform/lark/sender.go` (**modify**) — `apiSender` gains `Send` (create-in-chat, returns id) and `ReplyID` (threaded reply, returns id).
- `internal/platform/lark/render.go` (**modify**) — `sender` interface unchanged for `Renderer`; add a `groupSender` interface + `Renderer.groupSender()` accessor; `RenderPicker`/`RenderGroupConfirm` methods.
- `internal/platform/lark/groupreporter.go` (**create**) — `groupReporter` implementing `core.GroupReporter`.
- `internal/platform/lark/decode.go` (**modify**) — `pick_group` / `run_group` actions.
- `internal/platform/lark/dispatch.go` (**modify**) — `Adapter` gains `Groups`/`GroupRunner`/`GroupTimeout` + per-group gate; card-action routing branches `RunGroup` to `handleGroupRun`.
- `internal/groupapi/groupapi.go` (**create**) — concrete `GroupAPI` wiring `GroupRunner` + a Lark reporter factory for the endpoint/MCP (keeps `api`/`mcp` from importing `lark`).
- `internal/api/api.go` (**modify**) — `Register(mux, g, groups)` mounts `GET /api/v1/groups`, `POST /api/v1/groups/{name}/run`.
- `internal/mcp/server.go` (**modify**) — `NewServer(g, groups)` adds `list_groups`, `run_group`.
- `cmd/kato-bot/main.go` (**modify**) — build the `GroupRegistry`, `GroupRunner`, reporter factory, `groupapi`; inject into adapter, api, mcp.
- `charts/kato-bot/` (**modify**) — `values.yaml` `groups`, `templates/groups-configmap.yaml`, deployment mount + checksum + `KATO_GROUPS_FILE` env; `README.md` regen.
- `openapi.yaml` (**modify**) — `/api/v1/groups` routes.

---

## Task 1: Group config loading

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.GroupConfig{Name, Cluster, UseCase string; Concurrency int; LarkChatID string; Targets []map[string]string}`; `Config.Groups []GroupConfig`; `Config.GroupRunTimeout time.Duration`; `loadGroups(path string) ([]GroupConfig, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/config/config_test.go  (add)
func TestLoadGroups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.yaml")
	os.WriteFile(path, []byte(`
groups:
  - name: critical-services
    cluster: prod-1
    usecase: deployment-troubleshooting
    concurrency: 5
    larkChatId: oc_abc
    targets:
      - { namespace: payments, deployment: payment-api }
      - { namespace: cart, deployment: cart-api }
`), 0o600)

	groups, err := loadGroups(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Name != "critical-services" || g.Cluster != "prod-1" || g.UseCase != "deployment-troubleshooting" {
		t.Errorf("bad group header: %+v", g)
	}
	if g.Concurrency != 5 || g.LarkChatID != "oc_abc" {
		t.Errorf("bad group knobs: %+v", g)
	}
	if len(g.Targets) != 2 || g.Targets[0]["deployment"] != "payment-api" {
		t.Errorf("bad targets: %+v", g.Targets)
	}
}

func TestLoadGroupsValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "g.yaml")
		os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	cases := map[string]string{
		"dup name":      "groups:\n  - {name: a, cluster: c, usecase: u, targets: [{x: y}]}\n  - {name: a, cluster: c, usecase: u, targets: [{x: y}]}\n",
		"empty usecase": "groups:\n  - {name: a, cluster: c, usecase: '', targets: [{x: y}]}\n",
		"no targets":    "groups:\n  - {name: a, cluster: c, usecase: u, targets: []}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadGroups(write(body)); err == nil {
				t.Errorf("expected validation error for %s", name)
			}
		})
	}
}

func TestLoadGroupsMissingFileIsEmpty(t *testing.T) {
	groups, err := loadGroups(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing groups file must be non-fatal, got %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("groups = %d, want 0", len(groups))
	}
}
```

(Add imports `path/filepath`, `os` if not present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadGroups -v`
Expected: FAIL — `undefined: loadGroups`.

- [ ] **Step 3: Implement**

Add to `internal/config/config.go`. Note **groups are optional** (unlike clusters): a missing file yields zero groups, not an error, so existing deployments without groups keep working. Cluster cross-validation happens at startup in `main` (not here), because `loadGroups` doesn't know the cluster set.

```go
// GroupConfig is one predefined batch: a UseCase run across a static list of targets
// in one cluster.
type GroupConfig struct {
	Name        string
	Cluster     string
	UseCase     string
	Concurrency int
	LarkChatID  string
	Targets     []map[string]string
}

type groupsFile struct {
	Groups []struct {
		Name        string              `yaml:"name"`
		Cluster     string              `yaml:"cluster"`
		UseCase     string              `yaml:"usecase"`
		Concurrency int                 `yaml:"concurrency"`
		LarkChatID  string              `yaml:"larkChatId"`
		Targets     []map[string]string `yaml:"targets"`
	} `yaml:"groups"`
}

const defaultGroupConcurrency = 5

// loadGroups reads and validates the groups YAML file. A missing file is not an
// error (groups are optional) — it yields zero groups. Each group needs a unique
// non-empty name, a non-empty cluster and usecase, and at least one target.
func loadGroups(path string) ([]GroupConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read groups file %s: %w", path, err)
	}
	var f groupsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse groups file %s: %w", path, err)
	}
	seen := make(map[string]bool, len(f.Groups))
	out := make([]GroupConfig, 0, len(f.Groups))
	for i, g := range f.Groups {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return nil, fmt.Errorf("groups file %s: group #%d has an empty name", path, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("groups file %s: duplicate group name %q", path, name)
		}
		if strings.TrimSpace(g.Cluster) == "" {
			return nil, fmt.Errorf("groups file %s: group %q has an empty cluster", path, name)
		}
		if strings.TrimSpace(g.UseCase) == "" {
			return nil, fmt.Errorf("groups file %s: group %q has an empty usecase", path, name)
		}
		if len(g.Targets) == 0 {
			return nil, fmt.Errorf("groups file %s: group %q has no targets", path, name)
		}
		conc := g.Concurrency
		if conc < 1 {
			conc = defaultGroupConcurrency
		}
		seen[name] = true
		out = append(out, GroupConfig{
			Name: name, Cluster: strings.TrimSpace(g.Cluster), UseCase: strings.TrimSpace(g.UseCase),
			Concurrency: conc, LarkChatID: strings.TrimSpace(g.LarkChatID), Targets: g.Targets,
		})
	}
	return out, nil
}
```

Wire into `Load()`: after the clusters block, add
```go
	groups, err := loadGroups(envOr("KATO_GROUPS_FILE", "/etc/kato-bot/groups.yaml"))
	if err != nil {
		return Config{}, err
	}
	cfg.Groups = groups
```
and add a `GroupRunTimeout` field (default `1800 * time.Second`) parsed from `GROUP_RUN_TIMEOUT` with the same `time.ParseDuration` pattern as `KATO_RUN_TIMEOUT`. Add `Groups []GroupConfig` and `GroupRunTimeout time.Duration` to the `Config` struct.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): load and validate group definitions"
```

---

## Task 2: Core group types + registry + verdict on RunResult

**Files:**
- Create: `internal/core/group.go`
- Modify: `internal/core/types.go` (`RunResult`)
- Test: `internal/core/group_test.go`

**Interfaces:**
- Produces:
  - `RunResult` gains `Healthy *bool`, `Headline string`.
  - `core.Group{Name, Cluster, UseCase string; Concurrency int; LarkChatID string; Targets []map[string]string}`
  - `core.GroupRegistry` with `NewGroupRegistry()`, `Add(Group)`, `List() []Group`, `Get(name) (Group, bool)`, `ForCluster(cluster) []Group`.
  - `core.ServiceResult{Index int; Target map[string]string; Run, Phase, Summary, Warning string; Healthy *bool; Headline string; Err error}` with `Bucket() string`.
  - `core.GroupSummary{Group Group; Total, Healthy, Unhealthy, Errored, Unknown int}`.
  - `core.GroupDest{InReplyTo, ChatID string}`.
  - `core.GroupReporter` interface.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/group_test.go
package core

import "testing"

func TestServiceResultBucket(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name string
		r    ServiceResult
		want string
	}{
		{"errored", ServiceResult{Err: &RunError{Msg: "boom"}}, "errored"},
		{"unknown", ServiceResult{}, "unknown"},
		{"healthy", ServiceResult{Healthy: &tru}, "healthy"},
		{"unhealthy", ServiceResult{Healthy: &fls}, "unhealthy"},
		{"errored beats verdict", ServiceResult{Healthy: &tru, Err: &RunError{Msg: "x"}}, "errored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Bucket(); got != tt.want {
				t.Errorf("Bucket() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGroupRegistryForCluster(t *testing.T) {
	reg := NewGroupRegistry()
	reg.Add(Group{Name: "a", Cluster: "prod-1"})
	reg.Add(Group{Name: "b", Cluster: "prod-2"})
	reg.Add(Group{Name: "c", Cluster: "prod-1"})
	got := reg.ForCluster("prod-1")
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("ForCluster(prod-1) = %+v, want a,c in order", got)
	}
	if g, ok := reg.Get("b"); !ok || g.Cluster != "prod-2" {
		t.Errorf("Get(b) = %+v, %v", g, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'TestServiceResultBucket|TestGroupRegistryForCluster' -v`
Expected: FAIL — undefined types.

- [ ] **Step 3a: Extend RunResult in types.go**

In `internal/core/types.go`, add to `RunResult` (after `Warning`):
```go
	Healthy  *bool  // health verdict from kato: true/false; nil = unknown
	Headline string // one-line reason for Healthy; empty when unknown
```

- [ ] **Step 3b: Create group.go**

```go
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
	LarkChatID  string
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
// message (interactive) when InReplyTo is set, else post into ChatID (scheduled).
type GroupDest struct {
	InReplyTo string
	ChatID    string
}

// GroupReporter receives progress for one group run. GroupRunner calls these
// serially from one goroutine, so implementations need no internal locking.
type GroupReporter interface {
	Start(ctx context.Context, g Group, dest GroupDest, total int) error
	ServiceDone(ctx context.Context, g Group, r ServiceResult) error
	Finish(ctx context.Context, s GroupSummary) error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/core/ -run 'TestServiceResultBucket|TestGroupRegistryForCluster' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/core/group.go internal/core/types.go internal/core/group_test.go
git commit -m "feat(core): group types, registry, and health verdict on RunResult"
```

---

## Task 3: kato client carries the verdict

**Files:**
- Modify: `internal/kato/client.go` (`Run`, lines 145-172)
- Test: `internal/kato/client_test.go`

**Interfaces:**
- Consumes: `RunResult.Healthy/Headline` (Task 2).
- Produces: `Run` populates `Healthy`/`Headline` from the kato run response JSON.

- [ ] **Step 1: Write the failing test**

Add to `internal/kato/client_test.go` (mirror the existing `Run` test's httptest harness):

```go
func TestRunParsesVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"run":"r1","phase":"Succeeded","summary":"bad","healthy":false,"headline":"CrashLoopBackOff"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, false)
	res, err := c.Run(context.Background(), "uc", map[string]string{"namespace": "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Healthy == nil || *res.Healthy != false {
		t.Errorf("Healthy = %v, want false", res.Healthy)
	}
	if res.Headline != "CrashLoopBackOff" {
		t.Errorf("Headline = %q, want CrashLoopBackOff", res.Headline)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kato/ -run TestRunParsesVerdict -v`
Expected: FAIL — `res.Healthy` is nil (fields not decoded).

- [ ] **Step 3: Implement**

In `internal/kato/client.go` `Run`, extend the anonymous response struct and the returned `RunResult`:

```go
	var w struct {
		Run      string `json:"run"`
		Phase    string `json:"phase"`
		Summary  string `json:"summary"`
		Warning  string `json:"warning"`
		Healthy  *bool  `json:"healthy"`
		Headline string `json:"headline"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return core.RunResult{}, fmt.Errorf("decode run: %w", err)
	}
	return core.RunResult{
		Run: w.Run, Phase: w.Phase, Summary: w.Summary, Warning: w.Warning,
		Healthy: w.Healthy, Headline: w.Headline,
	}, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/kato/ -v`
Expected: PASS (including the existing `Run` test — older kato without the fields yields `Healthy=nil`).

- [ ] **Step 5: Commit**

```bash
git add internal/kato/client.go internal/kato/client_test.go
git commit -m "feat(kato): decode health verdict from run response"
```

---

## Task 4: GroupRunner (fan-out + retry + serialized reporting)

**Files:**
- Modify: `internal/core/group.go` (add `GroupRunner`)
- Test: `internal/core/grouprunner_test.go`

**Interfaces:**
- Consumes: `KatoClient` (types.go), `Registry` (registry.go), `GroupReporter`, `Group`, `ServiceResult` (Task 2).
- Produces: `core.GroupRunner{Clusters *Registry; MaxRetries int; Backoff func(int) time.Duration}` with `Run(ctx, g Group, dest GroupDest, reporter GroupReporter) error`.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/grouprunner_test.go
package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeKato struct {
	mu       sync.Mutex
	inFlight int32
	maxSeen  int32
	fail429  map[string]int // deployment -> remaining 429s before success
	verdict  map[string]bool
}

func (f *fakeKato) ListUseCases(ctx context.Context) ([]UseCase, error) { return nil, nil }
func (f *fakeKato) GetUseCase(ctx context.Context, name string) (Contract, error) {
	return Contract{}, nil
}
func (f *fakeKato) Run(ctx context.Context, name string, inputs map[string]string) (RunResult, error) {
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

func (e *statusErr) Error() string    { return "status" }
func (e *statusErr) HTTPStatus() int  { return e.code }
func (e *statusErr) Detail() string   { return "busy" }

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
	fk := &fakeKato{
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

	if err := gr.Run(context.Background(), g, GroupDest{ChatID: "oc"}, rep); err != nil {
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestGroupRunnerFanOut -v`
Expected: FAIL — `undefined: GroupRunner`.

- [ ] **Step 3: Implement `GroupRunner` in group.go**

```go
const defaultMaxGroupRetries = 3

// GroupRunner fans a group out to its cluster's KatoClient with a bounded worker
// pool, retrying kato 429/5xx, and drives a GroupReporter serially.
type GroupRunner struct {
	Clusters   *Registry
	MaxRetries int
	Backoff    func(attempt int) time.Duration
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -race -v`
Expected: PASS, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add internal/core/group.go internal/core/grouprunner_test.go
git commit -m "feat(core): GroupRunner bounded fan-out with retry"
```

---

## Task 5: Lark group cards

**Files:**
- Create: `internal/platform/lark/groupcards.go`
- Modify: `internal/platform/lark/cards.go` (`buildPickerCard`; add `buildGroupConfirmCard`, `buildGroupStartedCard`)
- Test: `internal/platform/lark/groupcards_test.go`

**Interfaces:**
- Consumes: existing `card2`, `markdown`, `button2`, `contextLines` helpers (cards.go); `core.Group`, `core.GroupSummary`, `core.ServiceResult`.
- Produces: `buildGroupParentCard(g core.Group, s core.GroupSummary, done int, final bool) string`; `buildServiceReplyCard(g core.Group, r core.ServiceResult) string`; `buildGroupConfirmCard(g core.Group) string`; `buildGroupStartedCard(g core.Group) string`; `buildPickerCard(cluster, ucs, groups)` extended.

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/lark/groupcards_test.go
package lark

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

func TestBuildGroupParentCardTallies(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", UseCase: "dt"}
	s := core.GroupSummary{Group: g, Total: 10, Healthy: 6, Unhealthy: 2, Errored: 1, Unknown: 1}
	card := buildGroupParentCard(g, s, 10, true)
	for _, want := range []string{"critical", "prod-1", "6", "2", "10"} {
		if !strings.Contains(card, want) {
			t.Errorf("parent card missing %q: %s", want, card)
		}
	}
}

func TestBuildServiceReplyCardBuckets(t *testing.T) {
	g := core.Group{Name: "critical", UseCase: "dt"}
	fls := false
	unhealthy := buildServiceReplyCard(g, core.ServiceResult{
		Target: map[string]string{"namespace": "payments", "deployment": "payment-api"},
		Healthy: &fls, Headline: "CrashLoopBackOff", Summary: "pods crashing",
	})
	if !strings.Contains(unhealthy, "payment-api") || !strings.Contains(unhealthy, "CrashLoopBackOff") {
		t.Errorf("unhealthy reply missing target/headline: %s", unhealthy)
	}
	errored := buildServiceReplyCard(g, core.ServiceResult{
		Target: map[string]string{"deployment": "auth-api"}, Err: &core.RunError{Msg: "kato busy"},
	})
	if !strings.Contains(errored, "auth-api") || !strings.Contains(errored, "kato busy") {
		t.Errorf("errored reply missing target/error: %s", errored)
	}
}

func TestBuildPickerCardHasGroupsSection(t *testing.T) {
	ucs := []core.UseCase{{Name: "dt", Description: "d", Ready: true}}
	groups := []core.Group{{Name: "critical", UseCase: "dt", Targets: []map[string]string{{"x": "y"}}}}
	card := buildPickerCard("prod-1", ucs, groups)
	if !strings.Contains(card, "critical") || !strings.Contains(card, "pick_group") {
		t.Errorf("picker card missing groups section: %s", card)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/lark/ -run 'Group|Picker' -v`
Expected: FAIL — undefined builders / `buildPickerCard` arity.

- [ ] **Step 3a: Extend buildPickerCard in cards.go**

Change the signature and append a Groups section (place after the usecases loop, before `return`):

```go
func buildPickerCard(cluster string, ucs []core.UseCase, groups []core.Group) string {
	elements := []any{markdown("🔧 **kato** — pick a troubleshooting flow")}
	elements = append(elements, contextLines(cluster, nil)...)
	for _, uc := range ucs {
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, markdown(fmt.Sprintf("**%s**\n%s", uc.Name, uc.Description)))
		if uc.Ready {
			elements = append(elements, button2("Select ▸", map[string]any{"action": "pick", "cluster": cluster, "usecase": uc.Name}))
		} else {
			elements = append(elements, markdown("_not ready (failed validation in cluster)_"))
		}
	}
	if len(groups) > 0 {
		elements = append(elements, map[string]any{"tag": "hr"}, markdown("**Groups**"))
		for _, g := range groups {
			elements = append(elements, markdown(fmt.Sprintf("**%s** · %s · %d targets", g.Name, g.UseCase, len(g.Targets))))
			elements = append(elements, button2("Run group ▸", map[string]any{"action": "pick_group", "cluster": cluster, "group": g.Name}))
		}
	}
	return card2("kato", elements)
}
```

Add `buildGroupConfirmCard` and `buildGroupStartedCard` to cards.go:

```go
func buildGroupConfirmCard(g core.Group) string {
	elements := []any{
		markdown(fmt.Sprintf("📦 **Group: %s**", g.Name)),
		markdown(fmt.Sprintf("Run **%s** across **%d** targets in **%s**?", g.UseCase, len(g.Targets), g.Cluster)),
		button2("Run ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name}),
	}
	return card2("kato", elements)
}

func buildGroupStartedCard(g core.Group) string {
	return card2("kato", []any{
		markdown(fmt.Sprintf("📦 **Group %s started** — running %s across %d targets. Results will appear in this thread.",
			g.Name, g.UseCase, len(g.Targets))),
	})
}
```

- [ ] **Step 3b: Create groupcards.go**

```go
// internal/platform/lark/groupcards.go
package lark

import (
	"fmt"
	"strings"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// buildGroupParentCard renders the progress/rollup card. done is how many of
// total have completed; final flips the header from "running" to "done".
func buildGroupParentCard(g core.Group, s core.GroupSummary, done int, final bool) string {
	head := "⏳"
	state := "running"
	if final {
		head = "✅"
		state = "done"
	}
	elements := []any{
		markdown(fmt.Sprintf("%s **Group: %s** — %s", head, g.Name, state)),
		markdown(fmt.Sprintf("%s · %s · %d targets", g.UseCase, g.Cluster, s.Total)),
		markdown(fmt.Sprintf("Progress: **%d/%d**", done, s.Total)),
		markdown(fmt.Sprintf("🟢 %d   🔴 %d   ⚠️ %d   ❔ %d", s.Healthy, s.Unhealthy, s.Errored, s.Unknown)),
	}
	return card2(g.Name, elements)
}

func targetLabel(t map[string]string) string {
	ns, dep := t["namespace"], t["deployment"]
	switch {
	case ns != "" && dep != "":
		return ns + "/" + dep
	case dep != "":
		return dep
	default:
		var parts []string
		for k, v := range t {
			parts = append(parts, k+"="+v)
		}
		return strings.Join(parts, " ")
	}
}

// buildServiceReplyCard renders one service's outcome as a threaded reply.
func buildServiceReplyCard(g core.Group, r core.ServiceResult) string {
	label := targetLabel(r.Target)
	if r.Err != nil {
		return card2(g.Name, []any{
			markdown(fmt.Sprintf("⚠️ **%s** — check failed to run", label)),
			markdown(r.Err.Error()),
		})
	}
	icon := "❔"
	switch r.Bucket() {
	case "healthy":
		icon = "🟢"
	case "unhealthy":
		icon = "🔴"
	}
	head := fmt.Sprintf("%s **%s**", icon, label)
	if r.Headline != "" {
		head += " — " + r.Headline
	}
	elements := []any{markdown(head)}
	if r.Warning != "" {
		elements = append(elements, markdown("⚠️ "+r.Warning))
	}
	elements = append(elements,
		map[string]any{"tag": "hr"},
		markdown("📋 **Summary**\n"+r.Summary),
		markdown("_run: "+r.Run+"_"),
	)
	return card2(g.Name, elements)
}
```

- [ ] **Step 3c: Fix the existing `buildPickerCard` caller**

`render.go`'s `RenderPicker` calls `buildPickerCard(...)`. It will be updated in Task 7; for now, to keep the build green, update its single call site to pass `nil` groups (Task 7 replaces it):

Run: `grep -rn "buildPickerCard(" internal/platform/lark`
Update the `render.go` call to `buildPickerCard(r.Cluster, ucs, nil)` (temporary; Task 7 threads real groups).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/platform/lark/ -run 'Group|Picker' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/lark/groupcards.go internal/platform/lark/cards.go internal/platform/lark/groupcards_test.go internal/platform/lark/render.go
git commit -m "feat(lark): group parent/service/confirm cards + picker groups section"
```

---

## Task 6: Lark sender id-returning primitives + group reporter

**Files:**
- Modify: `internal/platform/lark/sender.go` (add `Send`, `ReplyID` to `apiSender`)
- Modify: `internal/platform/lark/render.go` (add `groupSender` interface + `Renderer.GroupSender()` accessor)
- Create: `internal/platform/lark/groupreporter.go`
- Test: `internal/platform/lark/groupreporter_test.go`

**Interfaces:**
- Produces:
  - `groupSender` interface `{ Send(ctx, chatID, cardJSON) (string, error); ReplyID(ctx, toMessageID, cardJSON) (string, error); Patch(ctx, messageID, cardJSON) error }`, satisfied by `*apiSender`.
  - `func (rd *Renderer) GroupSender() groupSender` (type-asserts `rd.S`).
  - `newGroupReporter(s groupSender) *groupReporter` implementing `core.GroupReporter`.

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/lark/groupreporter_test.go
package lark

import (
	"context"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type fakeGroupSender struct {
	sent     []string // chatIDs sent to
	replies  []string // parent ids replied to
	patches  []string // ids patched
	nextID   int
}

func (f *fakeGroupSender) Send(ctx context.Context, chatID, card string) (string, error) {
	f.sent = append(f.sent, chatID)
	f.nextID++
	return "parent-1", nil
}
func (f *fakeGroupSender) ReplyID(ctx context.Context, to, card string) (string, error) {
	f.replies = append(f.replies, to)
	f.nextID++
	return "child", nil
}
func (f *fakeGroupSender) Patch(ctx context.Context, id, card string) error {
	f.patches = append(f.patches, id)
	return nil
}

func TestGroupReporterScheduledFlow(t *testing.T) {
	fs := &fakeGroupSender{}
	rep := newGroupReporter(fs)
	g := core.Group{Name: "critical", Cluster: "prod-1", UseCase: "dt", LarkChatID: "oc_x"}
	ctx := context.Background()

	if err := rep.Start(ctx, g, core.GroupDest{ChatID: "oc_x"}, 2); err != nil {
		t.Fatal(err)
	}
	tru := true
	rep.ServiceDone(ctx, g, core.ServiceResult{Target: map[string]string{"deployment": "a"}, Healthy: &tru})
	rep.ServiceDone(ctx, g, core.ServiceResult{Target: map[string]string{"deployment": "b"}, Err: &core.RunError{Msg: "x"}})
	rep.Finish(ctx, core.GroupSummary{Group: g, Total: 2, Healthy: 1, Errored: 1})

	if len(fs.sent) != 1 || fs.sent[0] != "oc_x" {
		t.Errorf("Start should Send once to the chat, got %v", fs.sent)
	}
	if len(fs.replies) != 2 {
		t.Errorf("expected 2 threaded replies, got %d", len(fs.replies))
	}
	for _, to := range fs.replies {
		if to != "parent-1" {
			t.Errorf("child replies must thread under the parent id, got %q", to)
		}
	}
	if len(fs.patches) == 0 || fs.patches[len(fs.patches)-1] != "parent-1" {
		t.Errorf("Finish should patch the parent card, patches=%v", fs.patches)
	}
}

func TestGroupReporterInteractiveUsesReply(t *testing.T) {
	fs := &fakeGroupSender{}
	rep := newGroupReporter(fs)
	g := core.Group{Name: "c"}
	if err := rep.Start(context.Background(), g, core.GroupDest{InReplyTo: "user-msg"}, 1); err != nil {
		t.Fatal(err)
	}
	if len(fs.sent) != 0 || len(fs.replies) != 1 || fs.replies[0] != "user-msg" {
		t.Errorf("interactive Start should ReplyID to the user msg, sent=%v replies=%v", fs.sent, fs.replies)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/lark/ -run GroupReporter -v`
Expected: FAIL — undefined `newGroupReporter` / `groupSender`.

- [ ] **Step 3a: Add id-returning primitives to sender.go**

`apiSender.Reply`/`Patch` already exist. Add `Send` (create message in a chat) and `ReplyID` (reply-in-thread returning the new id). Use the Lark SDK: create via `larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(...ReceiveId(chatID).MsgType("interactive").Content(cardJSON).Build()).Build()` then `s.cli.Im.V1.Message.Create(ctx, req)`; read the new id from `resp.Data.MessageId`. `ReplyID` mirrors the existing `Reply` but returns `*resp.Data.MessageId`.

```go
func (s *apiSender) Send(ctx context.Context, chatID, cardJSON string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).MsgType("interactive").Content(cardJSON).Build()).
		Build()
	resp, err := s.cli.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark create: %s (code %d)", resp.Msg, resp.Code)
	}
	return larkcore.StringValue(resp.Data.MessageId), nil
}

func (s *apiSender) ReplyID(ctx context.Context, toMessageID, cardJSON string) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(toMessageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").Content(cardJSON).ReplyInThread(true).Build()).
		Build()
	resp, err := s.cli.Im.V1.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark reply: %s (code %d)", resp.Msg, resp.Code)
	}
	return larkcore.StringValue(resp.Data.MessageId), nil
}
```

(Add the `larkcore "github.com/larksuite/oapi-sdk-go/v3/core"` import if not already present; confirm `resp.Data.MessageId` is `*string` — use `larkcore.StringValue` to deref safely.)

- [ ] **Step 3b: Add `groupSender` + accessor to render.go**

```go
// groupSender is the richer outbound surface the group reporter needs: it must
// create a parent card (returning its id) and thread replies under it.
type groupSender interface {
	Send(ctx context.Context, chatID, cardJSON string) (string, error)
	ReplyID(ctx context.Context, toMessageID, cardJSON string) (string, error)
	Patch(ctx context.Context, messageID, cardJSON string) error
}

// GroupSender exposes the underlying sender as a groupSender (the real *apiSender
// implements it). Panics only if wired with a sender that lacks the methods,
// which never happens in production (NewSender always uses *apiSender).
func (rd *Renderer) GroupSender() groupSender {
	return rd.S.(groupSender)
}
```

- [ ] **Step 3c: Create groupreporter.go**

```go
// internal/platform/lark/groupreporter.go
package lark

import (
	"context"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// groupReporter implements core.GroupReporter over Lark: a parent card plus one
// threaded reply per service, patching the parent's tallies as runs land.
// GroupRunner calls these serially, so no locking is needed.
type groupReporter struct {
	s        groupSender
	parentID string
	total    int
	done     int
	summ     core.GroupSummary
}

func newGroupReporter(s groupSender) *groupReporter { return &groupReporter{s: s} }

func (gr *groupReporter) Start(ctx context.Context, g core.Group, dest core.GroupDest, total int) error {
	gr.total = total
	gr.summ = core.GroupSummary{Group: g, Total: total}
	card := buildGroupParentCard(g, gr.summ, 0, false)
	var id string
	var err error
	if dest.InReplyTo != "" {
		id, err = gr.s.ReplyID(ctx, dest.InReplyTo, card)
	} else {
		id, err = gr.s.Send(ctx, dest.ChatID, card)
	}
	if err != nil {
		return err
	}
	gr.parentID = id
	return nil
}

func (gr *groupReporter) ServiceDone(ctx context.Context, g core.Group, r core.ServiceResult) error {
	gr.done++
	switch r.Bucket() {
	case "healthy":
		gr.summ.Healthy++
	case "unhealthy":
		gr.summ.Unhealthy++
	case "errored":
		gr.summ.Errored++
	default:
		gr.summ.Unknown++
	}
	// Post the service's full summary as a threaded reply under the parent.
	if _, err := gr.s.ReplyID(ctx, gr.parentID, buildServiceReplyCard(g, r)); err != nil {
		return err
	}
	// Update the parent card's progress/tallies. (One patch per service; if Lark
	// rate limits bite at ~100 targets, throttle this to every Nth call.)
	return gr.s.Patch(ctx, gr.parentID, buildGroupParentCard(g, gr.summ, gr.done, false))
}

func (gr *groupReporter) Finish(ctx context.Context, s core.GroupSummary) error {
	return gr.s.Patch(ctx, gr.parentID, buildGroupParentCard(s.Group, s, s.Total, true))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/platform/lark/ -run GroupReporter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/lark/sender.go internal/platform/lark/render.go internal/platform/lark/groupreporter.go internal/platform/lark/groupreporter_test.go
git commit -m "feat(lark): id-returning send/reply primitives + group reporter"
```

---

## Task 7: Lark flow wiring (picker groups, confirm, run)

**Files:**
- Modify: `internal/core/types.go` (`PickGroup`/`RunGroup` intents; `Renderer.RenderPicker` signature; add `RenderGroupConfirm`; `Core.Groups`)
- Modify: `internal/core/core.go` (`PickCluster` passes groups; new `PickGroup` case)
- Modify: `internal/platform/lark/render.go` (`RenderPicker` new arg; `RenderGroupConfirm`)
- Modify: `internal/platform/lark/decode.go` (`pick_group`/`run_group`)
- Modify: `internal/platform/lark/dispatch.go` (`Adapter` group fields + gate; route `RunGroup`)
- Test: `internal/platform/lark/decode_test.go`, `internal/core/core_test.go`

**Interfaces:**
- Consumes: `core.GroupRegistry`, `core.GroupRunner`, `newGroupReporter`, `Renderer.GroupSender()` (Tasks 2/4/6).
- Produces: `core.PickGroup{Reply; Name}`, `core.RunGroup{Reply; Name}`; `Renderer.RenderPicker(ctx, r, ucs, groups)`; `Renderer.RenderGroupConfirm(ctx, r, g)`; `Core.Groups *GroupRegistry`; `Adapter.Groups`, `Adapter.GroupRunner`, `Adapter.GroupTimeout`, `Adapter.newGroupReporter()`.

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/lark/decode_test.go  (add)
func TestDecodePickGroup(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"pick_group","cluster":"prod-1","group":"critical"}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := in.(core.PickGroup)
	if !ok {
		t.Fatalf("intent = %T, want core.PickGroup", in)
	}
	if pg.Name != "critical" || pg.Reply.Cluster != "prod-1" {
		t.Errorf("bad PickGroup: %+v", pg)
	}
}

func TestDecodeRunGroup(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"run_group","cluster":"prod-1","group":"critical"}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rg, ok := in.(core.RunGroup); !ok || rg.Name != "critical" {
		t.Fatalf("intent = %T (%+v), want core.RunGroup{critical}", in, in)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/lark/ -run 'DecodePickGroup|DecodeRunGroup' -v`
Expected: FAIL — undefined `core.PickGroup`/`core.RunGroup` and decoder cases.

- [ ] **Step 3a: Intents + Renderer + Core in core**

In `internal/core/types.go`:
- Add intents:
```go
type PickGroup struct {
	Reply Reply
	Name  string
}
type RunGroup struct {
	Reply Reply
	Name  string
}

func (PickGroup) isIntent() {}
func (RunGroup) isIntent()  {}
```
- Change the `Renderer` interface `RenderPicker` line to:
```go
	RenderPicker(ctx context.Context, r Reply, ucs []UseCase, groups []Group) error
	RenderGroupConfirm(ctx context.Context, r Reply, g Group) error
```
- Add `Groups *GroupRegistry` to the `Core` struct (in core.go).

In `internal/core/core.go`:
- In the `PickCluster` case, after fetching `ucs`, gather groups and pass them:
```go
		var groups []Group
		if c.Groups != nil {
			groups = c.Groups.ForCluster(v.Reply.Cluster)
		}
		return nil, c.R.RenderPicker(ctx, v.Reply, ucs, groups)
```
- Add a `PickGroup` case:
```go
	case PickGroup:
		if c.Groups == nil {
			return nil, c.R.RenderError(ctx, v.Reply, "groups are not configured")
		}
		g, ok := c.Groups.Get(v.Name)
		if !ok {
			return nil, c.R.RenderError(ctx, v.Reply, "unknown group "+v.Name)
		}
		return nil, c.R.RenderGroupConfirm(ctx, v.Reply, g)
```
`RunGroup` is intentionally NOT handled by `core.Handle` — the adapter routes it (Step 3c) because execution needs the Lark group reporter.

- [ ] **Step 3b: Renderer methods in render.go**

Update `RenderPicker` and add `RenderGroupConfirm`:
```go
func (rd *Renderer) RenderPicker(ctx context.Context, r core.Reply, ucs []core.UseCase, groups []core.Group) error {
	return rd.emit(ctx, r, buildPickerCard(r.Cluster, ucs, groups))
}

func (rd *Renderer) RenderGroupConfirm(ctx context.Context, r core.Reply, g core.Group) error {
	return rd.emit(ctx, r, buildGroupConfirmCard(g))
}
```
(Remove the temporary `nil` from Task 5 Step 3c.) The `captureRenderer` in cardaction.go also implements `core.Renderer` — add matching `RenderPicker(...groups)`/`RenderGroupConfirm` methods there (build into `cap.card`).

- [ ] **Step 3c: Decoder + adapter routing**

In `internal/platform/lark/decode.go`, read `group` and add cases:
```go
	group, _ := p.Action.Value["group"].(string)
	switch action {
	// ... existing pick_cluster / pick / run ...
	case "pick_group":
		return core.PickGroup{Reply: reply, Name: group}, nil
	case "run_group":
		return core.RunGroup{Reply: reply, Name: group}, nil
```

In `internal/platform/lark/dispatch.go`:
- Add fields to `Adapter`: `Groups *core.GroupRegistry`, `GroupRunner *core.GroupRunner`, `GroupTimeout time.Duration`, plus a gate: `groupMu sync.Mutex`, `groupInFlight map[string]bool`.
- In the `OnP2CardActionTrigger` handler, after decoding `intent`, branch:
```go
	if rg, ok := intent.(core.RunGroup); ok {
		return a.handleGroupRun(ctx, rg), nil
	}
	return a.handleCardAction(ctx, intent, replyOf(intent)), nil
```
- Add `handleGroupRun` + the gate helpers:
```go
func (a *Adapter) handleGroupRun(ctx context.Context, v core.RunGroup) *callback.CardActionTriggerResponse {
	g, ok := a.Groups.Get(v.Name)
	if !ok {
		return cardResponse(buildErrorCard("unknown group " + v.Name))
	}
	if !a.groupGate(g.Name) {
		return cardResponse(buildErrorCard("group " + g.Name + " is already running — wait for it to finish"))
	}
	reply := v.Reply
	go func() {
		defer a.groupUngate(g.Name)
		to := a.GroupTimeout
		if to <= 0 {
			to = 30 * time.Minute
		}
		bg, cancel := context.WithTimeout(context.Background(), to)
		defer cancel()
		reporter := newGroupReporter(a.R.GroupSender())
		// Interactive: thread the parent card under the confirm card's message.
		dest := core.GroupDest{InReplyTo: reply.MessageID}
		if err := a.GroupRunner.Run(bg, g, dest, reporter); err != nil {
			log.Printf("group run %s: %v", g.Name, err)
		}
	}()
	return cardResponse(buildGroupStartedCard(g))
}

func (a *Adapter) groupGate(name string) bool {
	a.groupMu.Lock()
	defer a.groupMu.Unlock()
	if a.groupInFlight == nil {
		a.groupInFlight = map[string]bool{}
	}
	if a.groupInFlight[name] {
		return false
	}
	a.groupInFlight[name] = true
	return true
}

func (a *Adapter) groupUngate(name string) {
	a.groupMu.Lock()
	delete(a.groupInFlight, name)
	a.groupMu.Unlock()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/core/ ./internal/platform/lark/ -race -v`
Expected: PASS. Update any existing `core_test.go` fake `Renderer` to the new `RenderPicker` signature + a `RenderGroupConfirm` stub (the build will point them out).

- [ ] **Step 5: Commit**

```bash
git add internal/core/types.go internal/core/core.go internal/platform/lark/render.go internal/platform/lark/decode.go internal/platform/lark/dispatch.go internal/platform/lark/cardaction.go internal/core/core_test.go internal/platform/lark/decode_test.go
git commit -m "feat(lark): group picker section, confirm, and async run"
```

---

## Task 8: Kick-off endpoint + MCP tool

**Files:**
- Create: `internal/groupapi/groupapi.go`
- Modify: `internal/api/api.go` (`Register` signature + routes)
- Modify: `internal/mcp/server.go` (`NewServer` signature + tools)
- Test: `internal/groupapi/groupapi_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Produces:
  - `api.GroupAPI` interface `{ ListJSON() []byte; Run(ctx, name string) *gateway.Error }` (nil = accepted/202).
  - `mcp` uses the same interface (aliased or shared) for `list_groups`/`run_group`.
  - `groupapi.Service` implementing it, built from `*core.GroupRunner`, `*core.GroupRegistry`, and a reporter factory `func() core.GroupReporter`.

- [ ] **Step 1: Write the failing test**

```go
// internal/groupapi/groupapi_test.go
package groupapi

import (
	"context"
	"testing"
	"time"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type nopReporter struct{}

func (nopReporter) Start(context.Context, core.Group, core.GroupDest, int) error { return nil }
func (nopReporter) ServiceDone(context.Context, core.Group, core.ServiceResult) error { return nil }
func (nopReporter) Finish(context.Context, core.GroupSummary) error { return nil }

func TestServiceRunUnknownGroup(t *testing.T) {
	svc := newTestService(t)
	if e := svc.Run(context.Background(), "nope"); e == nil || e.Status != 404 {
		t.Fatalf("Run(nope) = %v, want 404", e)
	}
}

func TestServiceRunMissingChatID(t *testing.T) {
	svc := newTestService(t) // group "nochat" has empty LarkChatID
	if e := svc.Run(context.Background(), "nochat"); e == nil || e.Status != 400 {
		t.Fatalf("Run(nochat) = %v, want 400", e)
	}
}

func TestServiceRunAccepted(t *testing.T) {
	svc := newTestService(t) // group "ok" has LarkChatID set
	done := make(chan struct{})
	svc.launch = func(f func()) { go func() { f(); close(done) }() }
	if e := svc.Run(context.Background(), "ok"); e != nil {
		t.Fatalf("Run(ok) = %v, want nil (accepted)", e)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("group run never launched")
	}
}
```

(`newTestService` builds a `Service` with a `GroupRegistry` holding groups `nochat` (no chat id) and `ok` (chat id set, one target), a `GroupRunner` over a fake `KatoClient` registry, and `reporterFactory` returning `nopReporter{}`. Model it on the `fakeKato`/`NewRegistry` setup from `internal/core/grouprunner_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/groupapi/ -v`
Expected: FAIL — package/types undefined.

- [ ] **Step 3a: Create groupapi.go**

```go
// internal/groupapi/groupapi.go
package groupapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
	"github.com/zufardhiyaulhaq/kato-bot/internal/gateway"
)

// Service starts group runs on behalf of the REST endpoint and MCP tool, posting
// results into each group's configured Lark chat. It is the non-interactive twin
// of the Lark adapter's handleGroupRun.
type Service struct {
	Groups   *core.GroupRegistry
	Runner   *core.GroupRunner
	NewReporter func() core.GroupReporter
	launch   func(func()) // overridable in tests; defaults to `go f()`
}

func New(groups *core.GroupRegistry, runner *core.GroupRunner, newReporter func() core.GroupReporter) *Service {
	return &Service{Groups: groups, Runner: runner, NewReporter: newReporter, launch: func(f func()) { go f() }}
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

// Run validates and launches a group asynchronously. nil = accepted (202).
func (s *Service) Run(ctx context.Context, name string) *gateway.Error {
	g, ok := s.Groups.Get(name)
	if !ok {
		return &gateway.Error{Status: http.StatusNotFound, Msg: "unknown group " + name}
	}
	if g.LarkChatID == "" {
		return &gateway.Error{Status: http.StatusBadRequest, Msg: "group " + name + " has no larkChatId; cannot post results"}
	}
	launch := s.launch
	if launch == nil {
		launch = func(f func()) { go f() }
	}
	group := g
	launch(func() {
		// A fresh, uncancelled context: the HTTP request returns 202 immediately.
		reporter := s.NewReporter()
		_ = s.Runner.Run(context.Background(), group, core.GroupDest{ChatID: group.LarkChatID}, reporter)
	})
	return nil
}
```

- [ ] **Step 3b: Wire the endpoint in api.go**

Add a `GroupAPI` interface and two routes to `Register`:
```go
type GroupAPI interface {
	ListJSON() []byte
	Run(ctx context.Context, name string) *gateway.Error
}

func Register(mux *http.ServeMux, g *gateway.Gateway, groups GroupAPI) {
	// ... existing routes ...
	mux.HandleFunc("GET /api/v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, groups.ListJSON())
	})
	mux.HandleFunc("POST /api/v1/groups/{name}/run", func(w http.ResponseWriter, r *http.Request) {
		if e := groups.Run(r.Context(), r.PathValue("name")); e != nil {
			writeGatewayErr(w, e)
			return
		}
		b, _ := json.Marshal(map[string]string{"status": "accepted", "group": r.PathValue("name")})
		writeRaw(w, http.StatusAccepted, b)
	})
}
```

- [ ] **Step 3c: Add MCP tools in server.go**

Extend `NewServer(g *gateway.Gateway, groups GroupAPI) *sdkmcp.Server` (reuse `api.GroupAPI` or declare a local interface). Register two tools:
- `list_groups`: inline handler returning `textResult(groups.ListJSON())`.
- `run_group`: input `{ Group string `json:"group" jsonschema:"the configured group name"` }`; handler calls `groups.Run(ctx, in.Group)`; on `*gateway.Error` return it (mapped), else `textResult([]byte("{\"status\":\"accepted\"}"))`. (This tool does NOT take a cluster — a group pins its own.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/groupapi/ ./internal/api/ ./internal/mcp/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/groupapi internal/api/api.go internal/mcp/server.go internal/api/api_test.go internal/groupapi/groupapi_test.go
git commit -m "feat(api,mcp): group list + async kick-off endpoint and tool"
```

---

## Task 9: Wiring, chart, docs, verification

**Files:**
- Modify: `cmd/kato-bot/main.go`
- Modify: `charts/kato-bot/values.yaml`, `charts/kato-bot/templates/groups-configmap.yaml` (**create**), `charts/kato-bot/templates/deployment.yaml`, `README.md` (regen)
- Modify: `openapi.yaml`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Wire main.go**

After building `registry`/`gw` from `cfg.Clusters`, add:
```go
	groupReg := core.NewGroupRegistry()
	for _, gc := range cfg.Groups {
		if _, ok := registry.Get(gc.Cluster); !ok {
			log.Fatalf("group %q references unknown cluster %q", gc.Name, gc.Cluster)
		}
		groupReg.Add(core.Group{
			Name: gc.Name, Cluster: gc.Cluster, UseCase: gc.UseCase,
			Concurrency: gc.Concurrency, LarkChatID: gc.LarkChatID, Targets: gc.Targets,
		})
	}
	groupRunner := &core.GroupRunner{Clusters: registry, MaxRetries: 3}
	newReporter := func() core.GroupReporter { return newGroupReporterForMain(renderer) }
```
Because `newGroupReporter` is unexported in `lark`, expose a small constructor from the lark package: add `func NewGroupReporter(r *Renderer) core.GroupReporter { return newGroupReporter(r.GroupSender()) }` in `groupreporter.go`, and in main use `lark.NewGroupReporter(renderer)`.

Set `c.Groups = groupReg` on the `core.Core`. Add to the `lark.Adapter` literal: `Groups: groupReg, GroupRunner: groupRunner, GroupTimeout: cfg.GroupRunTimeout`. Build `gapi := groupapi.New(groupReg, groupRunner, func() core.GroupReporter { return lark.NewGroupReporter(renderer) })`. Change the API listener block to `api.Register(apiMux, gw, gapi)` and `mcpserver.NewServer(gw, gapi)`.

- [ ] **Step 2: Chart — values, configmap, deployment**

`values.yaml` — add after the clusters block:
```yaml
# -- Predefined groups: run one usecase across a static list of targets in one
# cluster, reported natively into Lark. cluster must match a configured cluster.
# larkChatId (optional) is where the kick-off endpoint/MCP tool posts results.
groups: []
#  - name: critical-services
#    cluster: default
#    usecase: deployment-troubleshooting
#    concurrency: 5
#    larkChatId: oc_xxx
#    targets:
#      - { namespace: payments, deployment: payment-api }
# -- Overall timeout for one group run (Go duration).
groupRunTimeout: 1800s
```

Create `templates/groups-configmap.yaml`:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "kato-bot.name" . }}-groups
  labels:
    {{- include "kato-bot.labels" . | nindent 4 }}
data:
  groups.yaml: |
    groups:
    {{- range .Values.groups }}
      - name: {{ .name | quote }}
        cluster: {{ .cluster | quote }}
        usecase: {{ .usecase | quote }}
        {{- if .concurrency }}
        concurrency: {{ .concurrency }}
        {{- end }}
        {{- if .larkChatId }}
        larkChatId: {{ .larkChatId | quote }}
        {{- end }}
        targets:
        {{- range .targets }}
          - {{ toJson . }}
        {{- end }}
    {{- end }}
```

`deployment.yaml` — add a second checksum annotation, mount, and env:
```yaml
        checksum/groups: {{ include (print $.Template.BasePath "/groups-configmap.yaml") . | sha256sum }}
```
```yaml
        - name: groups
          configMap:
            name: {{ include "kato-bot.name" . }}-groups
```
```yaml
            - name: KATO_GROUPS_FILE
              value: /etc/kato-bot/groups.yaml
            - name: GROUP_RUN_TIMEOUT
              value: {{ .Values.groupRunTimeout | quote }}
```
```yaml
            - name: groups
              mountPath: /etc/kato-bot-groups
              readOnly: true
```
**Important:** the clusters ConfigMap already mounts at `/etc/kato-bot`. Two ConfigMaps cannot mount into the same directory, so mount groups at a distinct path (`/etc/kato-bot-groups`) and set `KATO_GROUPS_FILE: /etc/kato-bot-groups/groups.yaml`. Update the env value accordingly.

- [ ] **Step 3: openapi.yaml + README**

Add to `openapi.yaml`: `GET /api/v1/groups` (200 → `{groups:[{name,cluster,usecase,targets}]}`) and `POST /api/v1/groups/{name}/run` (202 → `{status,group}`, 400 no chat id, 404 unknown). Then:

Run: `make readme`

- [ ] **Step 4: Full verification**

Run: `go build ./... && go vet ./... && make test`
Expected: all PASS (race detector clean).

Run: `helm template charts/kato-bot --set groups[0].name=g --set groups[0].cluster=default --set groups[0].usecase=dt --set 'groups[0].targets[0].deployment=x' | grep -A20 groups.yaml`
Expected: the rendered `groups.yaml` contains the group and target.

- [ ] **Step 5: Commit**

```bash
git add cmd/kato-bot/main.go internal/platform/lark/groupreporter.go charts/kato-bot openapi.yaml README.md
git commit -m "feat: wire group runs (main, chart, openapi, docs)"
```

---

## Self-Review

- **Spec coverage:** config groups (Task 1); core types + registry (Task 2); verdict consumption (Tasks 2-3); GroupRunner fan-out/retry/buckets (Task 4); Lark parent+service+confirm cards & picker section (Task 5); reporter + threading primitives (Task 6); interactive flow incl. per-group gate (Task 7); endpoint + MCP (Task 8); chart/config/main/openapi/docs (Task 9). Four-way buckets appear in `ServiceResult.Bucket` (Task 2), tallies (Task 4), and both card + reporter (Tasks 5-6). The stateless/single-replica and "single-run untouched" constraints are respected (no DB; picker section is additive; `runSem` untouched).
- **Placeholder scan:** the only "copy the sibling harness" instructions are in test steps where the plan cannot see the existing test file (Task 3 Step 1, Task 8 Step 1's `newTestService`) — each names exactly what to copy and what to assert. No `TBD`/`add validation`/`handle edge cases`. All code steps carry real code.
- **Type consistency:** `ServiceResult`/`GroupSummary`/`GroupDest`/`GroupReporter` defined in Task 2, used identically in Tasks 4/6/8. `RunResult.Healthy *bool`/`Headline` added in Task 2, populated in Task 3, consumed in Task 4. `buildGroupParentCard(g, s, done, final)` / `buildServiceReplyCard(g, r)` signatures identical across Tasks 5-6. `GroupAPI{ListJSON, Run}` identical in Tasks 8 (api + mcp). `newGroupReporter`(unexported) + `NewGroupReporter`(exported wrapper) reconciled in Tasks 6/9. Mount-path collision between the two ConfigMaps is explicitly resolved in Task 9 Step 2.
