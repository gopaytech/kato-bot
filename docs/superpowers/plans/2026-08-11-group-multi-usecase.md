{% raw %}
# Group Multi-UseCase Implementation Plan (Part 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change a kato-bot Group from one UseCase over many targets to a `usecases:` list — each entry a UseCase with its own targets — flattened internally into work items, with every result labeled by its UseCase.

**Architecture:** `config.GroupConfig` gains `UseCases []GroupUseCase`; at startup main.go flattens the nested `usecases[].targets[]` into a flat `core.Group.Items []WorkItem`. The `GroupRunner` fans out over `Items` (each carries its own UseCase); `ServiceResult` gains `UseCase`. Reporting (Lark cards + JSON) labels each result by its UseCase and shows a per-UseCase breakdown. Migration is incremental: an additive `Group.Items` + `WorkItems()` shim keeps the build green while consumers migrate, then the old `UseCase`/`Targets` fields are removed in the final task.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, net/http (Go 1.22 routes), `go test -race`, Helm/helm-docs.

## Global Constraints

- **NO-COMMIT run.** Skip every "Commit" step. No `git add`/`git commit`/branches. Leave changes in the working tree — another agent shares this repo. Reviews run on the working-tree diff.
- **Bare `go` commands** (go on PATH is 1.25.2; builds fine). Do NOT edit `.tool-versions`.
- **Build stays green after every task** — that's why the migration is additive (add `Items`, migrate, remove old fields last).
- **No back-compat** with the old single-`usecase` config shape (the feature is unreleased). The final task removes `core.Group.UseCase`/`Targets` and `config.GroupConfig.UseCase`/`Targets`.
- **Interactive Lark path, async submit/poll, the in-flight gate, retry, and timeout all carry over unchanged** — this is a data-model change, not a control-flow change.
- Remove any stray `kato-bot` binary from the repo root if one appears after a build (`rm -f kato-bot`).

---

## File Structure

- `internal/core/group.go` (**modify**) — `WorkItem`, `Group.Items`, `Group.WorkItems()`, `Group.UseCaseCounts()` + `UseCaseCount`, `ServiceResult.UseCase`, runner fan-out over work items; (final task) remove `Group.UseCase`/`Targets`.
- `internal/config/config.go` (**modify**) — `GroupUseCase`, `GroupConfig.UseCases` (drop `UseCase`/`Targets`), `groupsFile` nested `usecases:`, rewritten `loadGroups` validation.
- `cmd/kato-bot/main.go` (**modify**) — flatten `gc.UseCases` → `core.Group.Items`.
- `internal/platform/lark/{groupcards.go, cards.go}` (**modify**) — per-UseCase service label; parent/confirm/started/picker show "N targets across M usecases".
- `internal/groupapi/groupapi.go` (**modify**) — `ServiceView.usecase`; group list view → `{name, cluster, usecases:[{usecase, targets}], totalTargets}`; drop the single group-wide `usecase` from `GroupResult`/`RunView`/`runRecord`/`groupView`.
- `openapi.yaml` (**modify**) — `ServiceView` + `usecase`; `GroupView` usecases breakdown; drop top-level `usecase` from `GroupResult`/`RunView`.
- `charts/kato-bot/templates/groups-configmap.yaml` + `values.yaml` (**modify**) — render nested `usecases:`.
- Tests alongside each.

---

## Task 1: core — work-item model (additive) + runner fan-out

**Files:**
- Modify: `internal/core/group.go`
- Test: `internal/core/group_test.go`, `internal/core/grouprunner_test.go`

**Interfaces:**
- Produces:
  - `type WorkItem struct { UseCase string; Inputs map[string]string }`
  - `Group` gains `Items []WorkItem` (keeps `UseCase`/`Targets` for now).
  - `func (g Group) WorkItems() []WorkItem` — returns `Items` if set, else expands `UseCase`×`Targets`.
  - `type UseCaseCount struct { UseCase string; Targets int }` + `func (g Group) UseCaseCounts() []UseCaseCount` (insertion-ordered, deduped).
  - `ServiceResult` gains `UseCase string`.

- [ ] **Step 1: Write the failing test**

```go
// internal/core/group_test.go  (add)
func TestGroupWorkItemsFromItems(t *testing.T) {
	g := Group{Items: []WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
	}}
	wi := g.WorkItems()
	if len(wi) != 2 || wi[0].UseCase != "dt" || wi[1].UseCase != "http" {
		t.Fatalf("WorkItems from Items = %+v", wi)
	}
}

func TestGroupWorkItemsFallbackFromTargets(t *testing.T) {
	g := Group{UseCase: "dt", Targets: []map[string]string{{"deployment": "a"}, {"deployment": "b"}}}
	wi := g.WorkItems()
	if len(wi) != 2 || wi[0].UseCase != "dt" || wi[0].Inputs["deployment"] != "a" || wi[1].Inputs["deployment"] != "b" {
		t.Fatalf("WorkItems fallback = %+v", wi)
	}
}

func TestGroupUseCaseCounts(t *testing.T) {
	g := Group{Items: []WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
		{UseCase: "dt", Inputs: map[string]string{"deployment": "b"}},
	}}
	got := g.UseCaseCounts()
	if len(got) != 2 || got[0].UseCase != "dt" || got[0].Targets != 2 || got[1].UseCase != "http" || got[1].Targets != 1 {
		t.Fatalf("UseCaseCounts = %+v (want dt:2, http:1 in order)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run 'WorkItems|UseCaseCounts' -v`
Expected: FAIL — `WorkItem`/`Items`/`WorkItems`/`UseCaseCounts` undefined.

- [ ] **Step 3: Implement in group.go**

Add the `Items` field to `Group` (leave `UseCase`/`Targets`):
```go
type Group struct {
	Name        string
	Cluster     string
	UseCase     string              // deprecated; removed in the final task
	Concurrency int
	Targets     []map[string]string // deprecated; removed in the final task
	Items       []WorkItem
}

// WorkItem is one unit of group work: a UseCase paired with the inputs it runs on.
type WorkItem struct {
	UseCase string
	Inputs  map[string]string
}

// WorkItems returns the flattened units of work. It prefers Items; when Items is
// empty it falls back to expanding the deprecated UseCase×Targets shape (removed
// once every caller sets Items).
func (g Group) WorkItems() []WorkItem {
	if len(g.Items) > 0 {
		return g.Items
	}
	out := make([]WorkItem, 0, len(g.Targets))
	for _, t := range g.Targets {
		out = append(out, WorkItem{UseCase: g.UseCase, Inputs: t})
	}
	return out
}

// UseCaseCount is one UseCase and how many targets it has within a group.
type UseCaseCount struct {
	UseCase string
	Targets int
}

// UseCaseCounts returns the distinct UseCases in insertion order with their
// target counts — for "N targets across M usecases" summaries and the group list.
func (g Group) UseCaseCounts() []UseCaseCount {
	var order []string
	counts := map[string]int{}
	for _, it := range g.WorkItems() {
		if _, ok := counts[it.UseCase]; !ok {
			order = append(order, it.UseCase)
		}
		counts[it.UseCase]++
	}
	out := make([]UseCaseCount, 0, len(order))
	for _, uc := range order {
		out = append(out, UseCaseCount{UseCase: uc, Targets: counts[uc]})
	}
	return out
}
```

Add `UseCase` to `ServiceResult` (after `Index`):
```go
type ServiceResult struct {
	Index    int
	UseCase  string
	Target   map[string]string
	Run      string
	Phase    string
	Summary  string
	Warning  string
	Healthy  *bool
	Headline string
	Err      error
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run 'WorkItems|UseCaseCounts' -v`
Expected: PASS.

- [ ] **Step 5: Migrate the runner to fan out over work items**

Add a runner test first (multi-usecase → each result carries its UseCase). Reuse the existing `groupFakeKato`/`recReporter` helpers in `grouprunner_test.go` (they capture `inputs["deployment"]`); add one:
```go
func TestGroupRunnerCarriesUseCase(t *testing.T) {
	fk := &groupFakeKato{verdict: map[string]bool{}}
	reg := NewRegistry()
	reg.Add(Cluster{Name: "prod-1"}, fk)
	g := Group{Name: "g", Cluster: "prod-1", Concurrency: 2, Items: []WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"deployment": "b"}},
	}}
	rep := &recReporter{}
	gr := &GroupRunner{Clusters: reg, MaxRetries: 3, Backoff: func(int) time.Duration { return time.Millisecond }}
	if err := gr.Run(context.Background(), g, GroupDest{}, rep); err != nil {
		t.Fatal(err)
	}
	byDep := map[string]string{} // deployment -> usecase
	for _, r := range rep.results {
		byDep[r.Target["deployment"]] = r.UseCase
	}
	if byDep["a"] != "dt" || byDep["b"] != "http" {
		t.Fatalf("usecase not carried per item: %+v", byDep)
	}
	if rep.total != 2 {
		t.Fatalf("total = %d, want 2", rep.total)
	}
}
```
Run it (FAIL — `r.UseCase` empty / feeder uses `g.UseCase`), then change `GroupRunner.Run`:
- `total := len(g.WorkItems())`
- the job type and feeder:
```go
	type job struct {
		idx  int
		item WorkItem
	}
	jobs := make(chan job)
	results := make(chan ServiceResult)

	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- gr.runOne(ctx, kc, maxRetries, j.idx, j.item)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i, it := range g.WorkItems() {
			select {
			case jobs <- job{idx: i, item: it}:
			case <-ctx.Done():
				return
			}
		}
	}()
```
- change `runOne` to take a `WorkItem` and stamp `UseCase`:
```go
func (gr *GroupRunner) runOne(ctx context.Context, kc KatoClient, maxRetries, idx int, item WorkItem) ServiceResult {
	sr := ServiceResult{Index: idx, UseCase: item.UseCase, Target: item.Inputs}
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
		res, err := kc.Run(ctx, item.UseCase, item.Inputs)
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
```
The existing `TestGroupRunnerFanOut` (which sets `Targets`) still passes via the `WorkItems()` fallback.

- [ ] **Step 6: Run all core tests (race)**

Run: `go build ./... && go test ./internal/core/ -race -v`
Expected: PASS, no races. (`go build ./...` still green — consumers still read the retained `UseCase`/`Targets` fields.)

- [ ] **Step 7: Commit** — SKIP (no-commit run).

---

## Task 2: config + main — `usecases:` list, flatten to Items

**Files:**
- Modify: `internal/config/config.go`, `cmd/kato-bot/main.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `core.WorkItem`, `core.Group.Items` (Task 1).
- Produces: `config.GroupUseCase{ UseCase string; Targets []map[string]string }`; `config.GroupConfig{ Name, Cluster string; Concurrency int; UseCases []GroupUseCase }` (no `UseCase`/`Targets`).

- [ ] **Step 1: Write the failing test**

Replace the group fixtures/asserts in `internal/config/config_test.go` to the nested shape. Add:
```go
func TestLoadGroupsMultiUseCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.yaml")
	os.WriteFile(path, []byte(`
groups:
  - name: critical
    cluster: prod-1
    concurrency: 5
    usecases:
      - usecase: deployment-troubleshooting
        targets:
          - { namespace: payments, deployment: payment-api }
          - { namespace: cart, deployment: cart-api }
      - usecase: http-connectivity-check
        targets:
          - { target: api, port: "443", scheme: https, path: /healthz }
`), 0o600)
	groups, err := loadGroups(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Name != "critical" || g.Cluster != "prod-1" || g.Concurrency != 5 {
		t.Errorf("bad header: %+v", g)
	}
	if len(g.UseCases) != 2 || g.UseCases[0].UseCase != "deployment-troubleshooting" ||
		len(g.UseCases[0].Targets) != 2 || g.UseCases[1].UseCase != "http-connectivity-check" ||
		g.UseCases[1].Targets[0]["target"] != "api" {
		t.Errorf("bad usecases: %+v", g.UseCases)
	}
}

func TestLoadGroupsValidationMulti(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string { p := filepath.Join(dir, "g.yaml"); os.WriteFile(p, []byte(body), 0o600); return p }
	cases := map[string]string{
		"no usecases":   "groups:\n  - {name: a, cluster: c, usecases: []}\n",
		"empty usecase": "groups:\n  - name: a\n    cluster: c\n    usecases:\n      - {usecase: '', targets: [{x: y}]}\n",
		"no targets":    "groups:\n  - name: a\n    cluster: c\n    usecases:\n      - {usecase: u, targets: []}\n",
		"dup name":      "groups:\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadGroups(write(body)); err == nil {
				t.Errorf("expected validation error for %s", name)
			}
		})
	}
}
```
Also update any existing group test that used the old `usecase:`/`targets:` top-level shape (e.g. `TestLoadGroups`, `TestLoadGroupsViaLoad`, `TestLoadGroupsMissingFileIsEmpty`) to the nested shape or to assert `UseCases`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run Group -v`
Expected: FAIL — `GroupConfig.UseCases`/`GroupUseCase` undefined.

- [ ] **Step 3: Implement config.go**

Replace the `GroupConfig` struct:
```go
// GroupConfig is one predefined batch: several UseCases, each run across its own
// list of targets, in one cluster.
type GroupConfig struct {
	Name        string
	Cluster     string
	Concurrency int
	UseCases    []GroupUseCase
}

// GroupUseCase is one UseCase within a group and the targets it runs on.
type GroupUseCase struct {
	UseCase string
	Targets []map[string]string
}
```
Replace the `groupsFile` struct's group entry:
```go
type groupsFile struct {
	Groups []struct {
		Name        string `yaml:"name"`
		Cluster     string `yaml:"cluster"`
		Concurrency int    `yaml:"concurrency"`
		UseCases    []struct {
			UseCase string              `yaml:"usecase"`
			Targets []map[string]string `yaml:"targets"`
		} `yaml:"usecases"`
	} `yaml:"groups"`
}
```
Rewrite the per-group body of `loadGroups` (keep the read/unmarshal/dup-name/empty-name/empty-cluster/concurrency-clamp scaffolding; replace the usecase/targets validation + mapping):
```go
		if len(g.UseCases) == 0 {
			return nil, fmt.Errorf("groups file %s: group %q has no usecases", path, name)
		}
		ucs := make([]GroupUseCase, 0, len(g.UseCases))
		for j, uc := range g.UseCases {
			un := strings.TrimSpace(uc.UseCase)
			if un == "" {
				return nil, fmt.Errorf("groups file %s: group %q usecase #%d has an empty usecase", path, name, j+1)
			}
			if len(uc.Targets) == 0 {
				return nil, fmt.Errorf("groups file %s: group %q usecase %q has no targets", path, name, un)
			}
			ucs = append(ucs, GroupUseCase{UseCase: un, Targets: uc.Targets})
		}
		conc := g.Concurrency
		if conc < 1 {
			conc = defaultGroupConcurrency
		}
		seen[name] = true
		out = append(out, GroupConfig{Name: name, Cluster: strings.TrimSpace(g.Cluster), Concurrency: conc, UseCases: ucs})
```
(Remove the old `g.UseCase`/`g.Targets` validation + the `UseCase:`/`Targets:` in the appended `GroupConfig`.)

- [ ] **Step 4: Flatten in main.go**

Replace the group registry loop in `cmd/kato-bot/main.go`:
```go
	groupReg := core.NewGroupRegistry()
	for _, gc := range cfg.Groups {
		if _, ok := registry.Get(gc.Cluster); !ok {
			log.Fatalf("group %q references unknown cluster %q", gc.Name, gc.Cluster)
		}
		var items []core.WorkItem
		for _, uc := range gc.UseCases {
			for _, t := range uc.Targets {
				items = append(items, core.WorkItem{UseCase: uc.UseCase, Inputs: t})
			}
		}
		groupReg.Add(core.Group{
			Name: gc.Name, Cluster: gc.Cluster, Concurrency: gc.Concurrency, Items: items,
		})
	}
	groupRunner := &core.GroupRunner{Clusters: registry, MaxRetries: 3}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/config/ -v`
Expected: PASS. `go build ./...` green — groupapi/lark still compile against the retained `core.Group.UseCase`/`Targets` (now unset at runtime; fixed in Tasks 3-4).

- [ ] **Step 6: Commit** — SKIP.

---

## Task 3: Lark cards — per-UseCase labels

**Files:**
- Modify: `internal/platform/lark/groupcards.go`, `internal/platform/lark/cards.go`
- Test: `internal/platform/lark/groupcards_test.go`

**Interfaces:**
- Consumes: `core.ServiceResult.UseCase`, `core.Group.WorkItems()`, `core.Group.UseCaseCounts()` (Task 1).

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/lark/groupcards_test.go  (add / adjust)
func TestServiceReplyCardLabelsUseCase(t *testing.T) {
	g := core.Group{Name: "critical"}
	fls := false
	card := buildServiceReplyCard(g, core.ServiceResult{
		UseCase: "deployment-troubleshooting",
		Target:  map[string]string{"namespace": "payments", "deployment": "payment-api"},
		Healthy: &fls, Headline: "CrashLoopBackOff", Summary: "x",
	})
	if !strings.Contains(card, "deployment-troubleshooting") || !strings.Contains(card, "payments/payment-api") {
		t.Errorf("service card missing usecase·target label: %s", card)
	}
}

func TestParentCardShowsUseCaseCount(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
	}}
	card := buildGroupParentCard(g, core.GroupSummary{Group: g, Total: 2}, 0, false)
	if !strings.Contains(card, "2 targets") || !strings.Contains(card, "2 usecases") {
		t.Errorf("parent card should show targets + usecase count: %s", card)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/lark/ -run 'ServiceReplyCardLabelsUseCase|ParentCardShowsUseCaseCount' -v`
Expected: FAIL (label lacks usecase; parent card prints `g.UseCase` + `%d targets` only).

- [ ] **Step 3: Implement**

In `groupcards.go` `buildServiceReplyCard`, build the label with the usecase prefix:
```go
	label := targetLabel(r.Target)
	if r.UseCase != "" {
		label = r.UseCase + " · " + label
	}
```
(Use `label` in both the errored and normal head lines as today.)

In `buildGroupParentCard`, replace the `g.UseCase · g.Cluster · N targets` line with cluster + targets + usecase count:
```go
		markdown(fmt.Sprintf("%s · %d targets across %d usecases", g.Cluster, s.Total, len(g.UseCaseCounts()))),
```

In `cards.go`:
- `buildGroupConfirmCard`: replace the "Run **usecase** across **N** targets in **cluster**?" line with a usecase-count phrasing and list the usecases:
```go
	elements := []any{
		markdown(fmt.Sprintf("📦 **Group: %s**", g.Name)),
		markdown(fmt.Sprintf("Run **%d** targets across **%d** usecases in **%s**?", len(g.WorkItems()), len(g.UseCaseCounts()), g.Cluster)),
	}
	for _, uc := range g.UseCaseCounts() {
		elements = append(elements, markdown(fmt.Sprintf("· %s (%d)", uc.UseCase, uc.Targets)))
	}
	elements = append(elements, button2("Run ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name}))
	return card2("kato", elements)
```
- `buildGroupStartedCard`: replace with `fmt.Sprintf("📦 **Group %s started** — running %d targets across %d usecases. Results will appear in this thread.", g.Name, len(g.WorkItems()), len(g.UseCaseCounts()))`.
- The picker Groups section in `buildPickerCard`: replace the per-group markdown line with:
```go
			elements = append(elements, markdown(fmt.Sprintf("**%s** · %s · %d targets · %d usecases", g.Name, g.Cluster, len(g.WorkItems()), len(g.UseCaseCounts()))))
```

Update any existing lark test that built a `core.Group{UseCase:..., Targets:...}` and asserted the old label to use `Items` and the new phrasing.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/platform/lark/ -v`
Expected: PASS.

- [ ] **Step 5: Commit** — SKIP.

---

## Task 4: groupapi + openapi — per-UseCase JSON

**Files:**
- Modify: `internal/groupapi/groupapi.go`, `openapi.yaml`
- Test: `internal/groupapi/groupapi_test.go`, `internal/api/api_test.go`, `internal/mcp/server_test.go`

**Interfaces:**
- Consumes: `core.ServiceResult.UseCase`, `core.Group.UseCaseCounts()`, `core.Group.WorkItems()` (Task 1).
- Produces: `ServiceView.UseCase` (`json:"usecase"`); group list view `{name, cluster, usecases:[{usecase, targets}], totalTargets}`; `GroupResult`/`RunView`/`runRecord` lose the single group-wide `usecase`.

- [ ] **Step 1: Write the failing test**

```go
// internal/groupapi/groupapi_test.go  (add / adjust)
func TestBuildGroupResultCarriesUseCase(t *testing.T) {
	g := core.Group{Name: "g", Cluster: "prod", Items: []core.WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
	}}
	tru := true
	rep := &core.CollectingReporter{
		Results: []core.ServiceResult{{UseCase: "dt", Target: map[string]string{"deployment": "a"}, Healthy: &tru}},
		Summary: core.GroupSummary{Group: g, Total: 1, Healthy: 1},
	}
	res := buildGroupResult(g, rep)
	if len(res.Services) != 1 || res.Services[0].UseCase != "dt" {
		t.Fatalf("service view usecase = %+v", res.Services)
	}
}

func TestListJSONUseCasesBreakdown(t *testing.T) {
	reg := core.NewGroupRegistry()
	reg.Add(core.Group{Name: "g", Cluster: "prod", Items: []core.WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
		{UseCase: "dt", Inputs: map[string]string{"deployment": "b"}},
	}})
	svc := New(reg, nil, time.Minute)
	var out struct {
		Groups []struct {
			Name         string `json:"name"`
			TotalTargets int    `json:"totalTargets"`
			UseCases     []struct {
				UseCase string `json:"usecase"`
				Targets int    `json:"targets"`
			} `json:"usecases"`
		} `json:"groups"`
	}
	json.Unmarshal(svc.ListJSON(), &out)
	if len(out.Groups) != 1 || out.Groups[0].TotalTargets != 3 || len(out.Groups[0].UseCases) != 2 ||
		out.Groups[0].UseCases[0].UseCase != "dt" || out.Groups[0].UseCases[0].Targets != 2 {
		t.Fatalf("list view = %+v", out.Groups)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/groupapi/ -run 'CarriesUseCase|Breakdown' -v`
Expected: FAIL — `ServiceView.UseCase` undefined; list view has a single `usecase`.

- [ ] **Step 3: Implement groupapi.go**

- `ServiceView`: add `UseCase string \`json:"usecase,omitempty"\`` (first field). In `buildGroupResult`, set `sv.UseCase = r.UseCase`. Remove `GroupResult.UseCase` (the struct field + the `UseCase: g.UseCase` in the returned literal).
- Group list view — replace `groupView` + `ListJSON`:
```go
type groupUseCaseView struct {
	UseCase string `json:"usecase"`
	Targets int    `json:"targets"`
}
type groupView struct {
	Name         string             `json:"name"`
	Cluster      string             `json:"cluster"`
	UseCases     []groupUseCaseView `json:"usecases"`
	TotalTargets int                `json:"totalTargets"`
}

func (s *Service) ListJSON() []byte {
	gs := s.Groups.List()
	views := make([]groupView, 0, len(gs))
	for _, g := range gs {
		ucs := make([]groupUseCaseView, 0)
		for _, c := range g.UseCaseCounts() {
			ucs = append(ucs, groupUseCaseView{UseCase: c.UseCase, Targets: c.Targets})
		}
		views = append(views, groupView{Name: g.Name, Cluster: g.Cluster, UseCases: ucs, TotalTargets: len(g.WorkItems())})
	}
	b, _ := json.Marshal(map[string]any{"groups": views})
	return b
}
```
- Remove the single group-wide `usecase` from `runRecord`, `RunView`, and `viewOf`: delete `runRecord.UseCase`, `RunView.UseCase` (+ its json), and the `UseCase: r.UseCase`/`UseCase: g.UseCase` assignments in `viewOf`/`Submit`. (A group no longer has one usecase; per-service usecase lives in `result.services[].usecase`, and the group list shows the breakdown.)

- [ ] **Step 4: Fix callers/tests + openapi**

- Update `internal/api/api_test.go` / `internal/mcp/server_test.go` fakes/asserts that reference `RunView.UseCase` or `groupView.UseCase` (drop them; assert the new shapes). The `GroupAPI` interface signatures are unchanged.
- `openapi.yaml`:
  - `ServiceView`: add `usecase` (string) property.
  - `GroupResult`: remove `usecase` from properties + `required`.
  - `RunView`: remove `usecase` from properties + `required`.
  - `GroupView`: replace the single `usecase`/`targets` with `usecases` (array of `{usecase, targets:int}`) + `totalTargets` (int); update the example.
  - Update the `listGroups` / run-view examples accordingly.

- [ ] **Step 5: Run tests (race)**

Run: `go build ./... && go test ./internal/groupapi/ ./internal/api/ ./internal/mcp/ -race -v`
Expected: PASS.

- [ ] **Step 6: Commit** — SKIP.

---

## Task 5: cleanup + chart + docs + verification

**Files:**
- Modify: `internal/core/group.go` (remove deprecated fields), `charts/kato-bot/templates/groups-configmap.yaml`, `charts/kato-bot/values.yaml`, spec doc, `README.md`
- Test: whole suite

**Interfaces:**
- Consumes: everything above (every runtime path now sets `Group.Items`).

- [ ] **Step 1: Remove the deprecated fields + shim fallback**

In `internal/core/group.go`:
- Remove `Group.UseCase` and `Group.Targets`.
- Simplify `WorkItems()` to `func (g Group) WorkItems() []WorkItem { return g.Items }`.

Run: `go build ./... 2>&1 | head` — fix any remaining reference (there should be none: main sets `Items`; lark/groupapi migrated in Tasks 3-4; tests migrated). Also `grep -rn "\.UseCase\b\|\.Targets\b" internal/core internal/platform/lark internal/groupapi cmd | grep -v "UseCase:" | grep -vi test` to catch stragglers (the config `GroupConfig.UseCases`/`GroupUseCase.UseCase` and `WorkItem.UseCase` are fine).

- [ ] **Step 2: Chart — render nested `usecases:`**

`charts/kato-bot/templates/groups-configmap.yaml` — replace the group body:
```yaml
data:
  groups.yaml: |
    groups:
    {{- range .Values.groups }}
      - name: {{ .name | quote }}
        cluster: {{ .cluster | quote }}
        {{- if .concurrency }}
        concurrency: {{ .concurrency }}
        {{- end }}
        usecases:
        {{- range .usecases }}
          - usecase: {{ .usecase | quote }}
            targets:
            {{- range .targets }}
              - {{ toJson . }}
            {{- end }}
        {{- end }}
    {{- end }}
```
`charts/kato-bot/values.yaml` — update the commented example to the nested shape:
```yaml
# -- Predefined groups: several usecases (each with its own targets) in one
# cluster. cluster must match a configured cluster.
groups: []
#  - name: critical-services
#    cluster: default
#    concurrency: 5
#    usecases:
#      - usecase: deployment-troubleshooting
#        targets:
#          - { namespace: payments, deployment: payment-api }
#      - usecase: http-connectivity-check
#        targets:
#          - { target: api.internal, port: "443", scheme: https, path: /healthz }
```

- [ ] **Step 3: Spec doc**

In `docs/superpowers/specs/2026-08-11-group-multi-usecase-and-summary-design.md`, mark Part 1 as implemented (a one-line note at the top of Part 1) — no behavioral change, just keeping the doc honest.

- [ ] **Step 4: Regenerate README + full verification**

Run:
```
make readme
go build ./... && go vet ./... && go test ./... -race -count=1
helm template charts/kato-bot --set lark.appId=x --set lark.appSecret=y \
  --set groups[0].name=g --set groups[0].cluster=default \
  --set groups[0].usecases[0].usecase=dt \
  --set 'groups[0].usecases[0].targets[0].deployment=x' | grep -A12 'groups.yaml'
```
Expected: all tests green (race clean); the rendered `groups.yaml` shows the nested `usecases:` → `targets:` structure. Remove any stray root `kato-bot` binary.

- [ ] **Step 5: Commit** — SKIP.

---

## Self-Review

- **Spec coverage (Part 1):** config `usecases:` list + validation (Task 2); flatten to `Items` (Tasks 1-2); `ServiceResult.UseCase` + runner fan-out (Task 1); per-usecase Lark labels + "N targets across M usecases" (Task 3); `ServiceView.usecase` + group list breakdown, drop group-wide usecase (Task 4); chart nested render + values (Task 5); no back-compat / old fields removed (Task 5). One cluster per group, group-level concurrency, no cartesian — all preserved (unchanged runner/gate/timeout).
- **Placeholder scan:** the only "update existing test X" instructions (Tasks 2-4) name the exact files and the exact new shape to assert; all code steps carry real code. No `TBD`/`add validation`/`handle edge cases`.
- **Type consistency:** `WorkItem{UseCase, Inputs}`, `Group.Items`, `WorkItems()`, `UseCaseCounts()`/`UseCaseCount{UseCase, Targets}`, `ServiceResult.UseCase` defined in Task 1 and consumed identically in Tasks 2-5. `runOne(ctx, kc, maxRetries, idx, item WorkItem)` new signature is self-consistent within Task 1. `ServiceView.UseCase`/`groupView.UseCases` (Task 4) match the openapi changes in the same task. Deprecated `Group.UseCase`/`Targets` exist Tasks 1-4 and are removed in Task 5 once every consumer sets `Items`.
{% endraw %}