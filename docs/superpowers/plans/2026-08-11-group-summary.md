{% raw %}
# Group Summary (LLM) Implementation Plan (Part 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give kato-bot its first LLM client and, when a group run is requested with a summary flag, synthesize an LLM narrative over the collected per-service results — returned in the JSON result (async path) and posted as a final Lark reply (interactive path). Optional, opt-in, non-fatal.

**Architecture:** A new platform-agnostic `internal/summary` package holds an OpenAI-compatible client (mirrored from kato's `summarizer.OpenAIClient`) plus a bounded prompt builder and a total `Summarize(...)` helper. It's injected (nil when unconfigured) into `groupapi.Service` and the Lark `Adapter`. `Submit` gains a `summary bool`; the endpoint reads `?summary=true`, the `run_group` MCP tool a `summary` bool, and the Lark confirm card gets a second "Run + summary" button (threaded via a `RunGroup.Summary` flag). Config comes from a `groupSummary:` Helm block → env, with the API key riding the existing `envFrom.secretRef` Secret.

**Tech Stack:** Go 1.25, net/http, OpenAI-compatible chat completions, `go test -race`, Helm/helm-docs.

## Global Constraints

- **NO-COMMIT run.** Skip every "Commit" step. No `git add`/`git commit`/branches. Leave changes in the working tree — another agent shares the repo. Reviews run on the working-tree diff.
- **Bare `go` commands** (go on PATH is 1.25.2). Do NOT edit `.tool-versions`. gofmt-clean every touched `.go` file. `rm -f kato-bot` if a stray root binary appears.
- **Build stays green after every task.** The summarizer is a nil-safe injected dependency: an unconfigured/nil client is not an error — a requested summary yields empty `summary` + a `summaryWarning`, and the run still succeeds.
- **Non-fatal, advisory** — mirrors kato's summary contract. A failed LLM call NEVER aborts a group run or changes tallies/verdicts.
- **`internal/summary`, `internal/groupapi`, `internal/api`, `internal/mcp` import NO `internal/platform/lark`.** The summarizer is injected from main.go.
- **Opt-in, group-only.** The single-usecase run flow is untouched. Summary runs only when the per-run flag is set OR the group's config default `summary: true` is set.
- **Bounded prompt.** One line per service (`usecase · target · bucket · headline`) for all; the truncated per-service `summary` for NON-healthy services only; capped to `GROUP_SUMMARY_MAX_EVIDENCE_BYTES` (default 16384).

---

## File Structure

- `internal/summary/openai.go` (**create**) — `Client` interface + `OpenAIClient` (mirror of kato's).
- `internal/summary/summary.go` (**create**) — `BuildEvidence`, `Summarize(ctx, client, group, results) (text, warning string)`, the system prompt, the byte cap.
- `internal/config/config.go` (**modify**) — `GroupSummaryConfig` + `Config.GroupSummary`; `GROUP_SUMMARY_*` env; per-group `Summary bool` in `GroupConfig`/`GroupUseCase`-less (group-level).
- `internal/core/group.go` (**modify**) — `Group.Summary bool` (config default).
- `internal/groupapi/groupapi.go` (**modify**) — `Service.Summarizer summary.Client`; `Submit(name string, doSummary bool)`; `runRecord`/`GroupResult` gain `Summary`/`SummaryWarning`; summarize after a successful run.
- `internal/api/api.go` + `internal/mcp/server.go` (**modify**) — `GroupAPI.Submit(name, summary bool)`; endpoint `?summary=true`; `runGroupIn.Summary`.
- `internal/core/types.go` (**modify**) — `RunGroup.Summary bool`.
- `internal/platform/lark/{decode.go, cards.go, dispatch.go, groupreporter.go}` (**modify**) — decode `summary`; "Run + summary" button; `groupReporter` accumulates `Results`; `handleGroupRun` posts a final summary reply; `Adapter.Summarizer`.
- `cmd/kato-bot/main.go` (**modify**) — build `*summary.OpenAIClient` from config (nil if disabled), inject into `groupapi.New` and `Adapter`.
- `charts/kato-bot/{values.yaml, templates/secret.yaml, templates/deployment.yaml}` (**modify**) — `groupSummary:` config + `GROUP_SUMMARY_API_KEY` secret key + env.
- `openapi.yaml` (**modify**) — `summary`/`summaryWarning` on `GroupResult`; `?summary` query param on the run op.
- Tests alongside each.

---

## Task 1: `internal/summary` — LLM client + bounded prompt + Summarize

**Files:**
- Create: `internal/summary/openai.go`, `internal/summary/summary.go`
- Test: `internal/summary/summary_test.go`

**Interfaces:**
- Produces:
  - `type Client interface { Complete(ctx context.Context, system, user string) (string, error) }`
  - `type OpenAIClient struct { BaseURL, Model, APIKey string; MaxTokens int; Temperature float64; Timeout time.Duration; HTTPClient *http.Client }` implementing `Complete`.
  - `func Summarize(ctx context.Context, client Client, g core.Group, results []core.ServiceResult, maxEvidenceBytes int) (summary string, warning string)`
  - `const DefaultMaxEvidenceBytes = 16384`

- [ ] **Step 1: Write the failing test**

```go
// internal/summary/summary_test.go
package summary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type fakeClient struct {
	gotSystem, gotUser string
	out                string
	err                error
}

func (f *fakeClient) Complete(ctx context.Context, system, user string) (string, error) {
	f.gotSystem, f.gotUser = system, user
	return f.out, f.err
}

func boolPtr(b bool) *bool { return &b }

func results() []core.ServiceResult {
	f := false
	return []core.ServiceResult{
		{UseCase: "dt", Target: map[string]string{"namespace": "payments", "deployment": "payment-api"}, Healthy: boolPtr(f), Headline: "CrashLoopBackOff", Summary: "pods crashing on bad image"},
		{UseCase: "http", Target: map[string]string{"target": "api"}, Healthy: boolPtr(true), Headline: "ok"},
	}
}

func TestSummarizeCallsClientWithBoundedEvidence(t *testing.T) {
	c := &fakeClient{out: "18/20 healthy; payment-api crashlooping."}
	g := core.Group{Name: "critical", Cluster: "prod-1"}
	sum, warn := Summarize(context.Background(), c, g, results(), DefaultMaxEvidenceBytes)
	if warn != "" {
		t.Fatalf("warning = %q, want empty", warn)
	}
	if sum != "18/20 healthy; payment-api crashlooping." {
		t.Fatalf("summary = %q", sum)
	}
	// The evidence must include each service line, and the non-healthy service's summary.
	if !strings.Contains(c.gotUser, "payments/payment-api") || !strings.Contains(c.gotUser, "CrashLoopBackOff") {
		t.Errorf("evidence missing unhealthy line: %s", c.gotUser)
	}
	if !strings.Contains(c.gotUser, "pods crashing on bad image") {
		t.Errorf("evidence should include the non-healthy per-service summary: %s", c.gotUser)
	}
	if !strings.Contains(c.gotSystem, "SRE") {
		t.Errorf("system prompt missing: %s", c.gotSystem)
	}
}

func TestSummarizeNilClientIsNotConfigured(t *testing.T) {
	sum, warn := Summarize(context.Background(), nil, core.Group{}, results(), DefaultMaxEvidenceBytes)
	if sum != "" || warn == "" {
		t.Fatalf("nil client should yield empty summary + a warning, got sum=%q warn=%q", sum, warn)
	}
}

func TestSummarizeClientErrorIsNonFatalWarning(t *testing.T) {
	c := &fakeClient{err: errors.New("LLM down")}
	sum, warn := Summarize(context.Background(), c, core.Group{}, results(), DefaultMaxEvidenceBytes)
	if sum != "" || !strings.Contains(warn, "LLM down") {
		t.Fatalf("client error should yield empty summary + warning carrying the error, got sum=%q warn=%q", sum, warn)
	}
}

func TestBuildEvidenceCapsBytes(t *testing.T) {
	f := false
	var many []core.ServiceResult
	for i := 0; i < 500; i++ {
		many = append(many, core.ServiceResult{UseCase: "dt", Target: map[string]string{"deployment": "d"}, Healthy: &f, Headline: "bad", Summary: strings.Repeat("x", 200)})
	}
	ev := BuildEvidence(many, 4096)
	if len(ev) > 4096+64 { // small allowance for the truncation marker line
		t.Fatalf("evidence not capped: %d bytes", len(ev))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/summary/ -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Create openai.go (mirror kato's client)**

```go
// internal/summary/openai.go
package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is the minimal LLM capability the group summarizer needs.
type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

const defaultLLMTimeout = 120 * time.Second

// OpenAIClient talks to any OpenAI-compatible /chat/completions endpoint.
type OpenAIClient struct {
	BaseURL     string
	Model       string
	APIKey      string
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
	HTTPClient  *http.Client
}

func (c *OpenAIClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	t := c.Timeout
	if t <= 0 {
		t = defaultLLMTimeout
	}
	return &http.Client{Timeout: t}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func (c *OpenAIClient) Complete(ctx context.Context, system, user string) (string, error) {
	reqBody := chatRequest{
		Model: c.Model, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
		Messages: []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("call LLM: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned %s: %s", strconv.Itoa(resp.StatusCode), strings.TrimSpace(string(raw)))
	}
	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
```

- [ ] **Step 4: Create summary.go (prompt + Summarize)**

```go
// internal/summary/summary.go
package summary

import (
	"context"
	"fmt"
	"strings"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// DefaultMaxEvidenceBytes bounds the evidence sent to the LLM.
const DefaultMaxEvidenceBytes = 16384

const systemPrompt = `You are a Kubernetes SRE. You are given the per-service results of a batch of troubleshooting checks run across a group. Write a concise group health summary: the overall status (how many healthy vs not), the notable failures and any common theme, and the single most useful next action. Use ONLY the evidence provided; do not invent data.`

// BuildEvidence renders the group's results as a bounded evidence block: one line
// per service (usecase · target · bucket · headline), plus the truncated
// per-service summary for non-healthy services, capped to maxBytes.
func BuildEvidence(results []core.ServiceResult, maxBytes int) string {
	var b strings.Builder
	truncated := false
	for _, r := range results {
		line := fmt.Sprintf("- %s · %s · %s", r.UseCase, targetLabel(r.Target), r.Bucket())
		if r.Headline != "" {
			line += " — " + r.Headline
		}
		line += "\n"
		if r.Bucket() != "healthy" && strings.TrimSpace(r.Summary) != "" {
			line += "    " + oneLine(r.Summary) + "\n"
		}
		if b.Len()+len(line) > maxBytes {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	if truncated {
		b.WriteString("[... evidence truncated to fit budget ...]\n")
	}
	return b.String()
}

// Summarize builds the prompt and calls the client. It is total: a nil client
// (unconfigured) or a client error yields an empty summary and a non-empty
// warning — never an error, never a panic.
func Summarize(ctx context.Context, client Client, g core.Group, results []core.ServiceResult, maxEvidenceBytes int) (summary string, warning string) {
	if client == nil {
		return "", "group summary not configured"
	}
	if maxEvidenceBytes <= 0 {
		maxEvidenceBytes = DefaultMaxEvidenceBytes
	}
	user := fmt.Sprintf("Group: %s (cluster %s)\n\nResults:\n%s", g.Name, g.Cluster, BuildEvidence(results, maxEvidenceBytes))
	out, err := client.Complete(ctx, systemPrompt, user)
	if err != nil {
		return "", "group summary unavailable: " + err.Error()
	}
	return strings.TrimSpace(out), ""
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

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/summary/ -v`
Expected: PASS.

- [ ] **Step 6: Commit** — SKIP.

---

## Task 2: groupapi async path — summarize on submit

**Files:**
- Modify: `internal/groupapi/groupapi.go`, `internal/api/api.go`, `internal/mcp/server.go`
- Test: `internal/groupapi/groupapi_test.go`, `internal/api/api_test.go`, `internal/mcp/server_test.go`

**Interfaces:**
- Consumes: `summary.Client`, `summary.Summarize` (Task 1).
- Produces: `Service.Summarizer summary.Client`; `Submit(name string, doSummary bool) (string, *gateway.Error)`; `GroupResult` gains `Summary string \`json:"summary,omitempty"\``, `SummaryWarning string \`json:"summaryWarning,omitempty"\``; `runRecord` gains `Summary`, `SummaryWarning`; `GroupAPI.Submit(name string, summary bool)` in both api + mcp.

- [ ] **Step 1: Write the failing test**

```go
// internal/groupapi/groupapi_test.go  (add)
type stubSummarizer struct{ out string; err error }
func (s stubSummarizer) Complete(ctx context.Context, system, user string) (string, error) { return s.out, s.err }

func TestSubmitWithSummaryPopulatesResult(t *testing.T) {
	// build a Service whose Runner runs a 1-target group over a fake KatoClient
	// (reuse the existing newTestService helper's registry/runner), and set:
	svc := newTestService(t)                    // group "ok" exists, 1+ targets
	svc.Summarizer = stubSummarizer{out: "1 healthy, all good."}
	runID, e := svc.Submit("ok", true)
	if e != nil { t.Fatalf("submit: %v", e) }
	waitForStatus(t, svc, runID, "done")        // reuse the existing poll helper
	view, _ := svc.GetRun(runID)
	if view.Result == nil || view.Result.Summary != "1 healthy, all good." {
		t.Fatalf("summary not in result: %+v", view.Result)
	}
	if view.Result.SummaryWarning != "" {
		t.Errorf("unexpected warning: %q", view.Result.SummaryWarning)
	}
}

func TestSubmitWithoutSummarySkips(t *testing.T) {
	svc := newTestService(t)
	svc.Summarizer = stubSummarizer{out: "should not be called"}
	runID, _ := svc.Submit("ok", false)
	waitForStatus(t, svc, runID, "done")
	view, _ := svc.GetRun(runID)
	if view.Result.Summary != "" {
		t.Errorf("summary should be empty when not requested: %q", view.Result.Summary)
	}
}

func TestSubmitSummaryRequestedButNilSummarizerWarns(t *testing.T) {
	svc := newTestService(t) // Summarizer left nil
	runID, _ := svc.Submit("ok", true)
	waitForStatus(t, svc, runID, "done")
	view, _ := svc.GetRun(runID)
	if view.Result.Summary != "" || view.Result.SummaryWarning == "" {
		t.Errorf("nil summarizer + requested → empty summary + warning, got %+v", view.Result)
	}
}
```

(Adjust to the exact `newTestService`/`waitForStatus` helper names in the file. If `Submit`'s new 2nd arg breaks other existing `Submit(...)` calls in the test file, update them to `Submit(name, false)`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/groupapi/ -run Summary -v`
Expected: FAIL — `Service.Summarizer` undefined; `Submit` arity.

- [ ] **Step 3: Implement groupapi.go**

- Add to `Service`: `Summarizer summary.Client` (import `internal/summary`). Also add an evidence cap field `SummaryMaxEvidenceBytes int` (0 → `summary.DefaultMaxEvidenceBytes`). `New` keeps its signature (Summarizer set by main as a field; nil-safe).
- Add to `GroupResult`: `Summary string \`json:"summary,omitempty"\`` and `SummaryWarning string \`json:"summaryWarning,omitempty"\``.
- Add to `runRecord`: `Summary string`, `SummaryWarning string` (not strictly needed if stored on Result, but keep for clarity — set them on `rec.Result` directly).
- Change `Submit(name string) (string, *gateway.Error)` → `Submit(name string, doSummary bool) (string, *gateway.Error)`. In the background goroutine's success branch, after `rec.Result = buildGroupResult(g, rep)`:
```go
			rec.Result = buildGroupResult(g, rep)
			if doSummary {
				sum, warn := summary.Summarize(ctx, s.Summarizer, g, rep.Results, s.SummaryMaxEvidenceBytes)
				rec.Result.Summary = sum
				rec.Result.SummaryWarning = warn
			}
```
(The summarize call uses the same `ctx` — bounded by the run timeout. It runs on the background goroutine, after the fan-out, before the terminal status write, all under the existing lock section is fine since the LLM call is BEFORE `s.mu.Lock()` — keep the summarize call OUTSIDE the lock, then lock to write status. Restructure: compute `res := buildGroupResult(...)`, `if doSummary { res.Summary, res.SummaryWarning = summary.Summarize(...) }` BEFORE taking `s.mu.Lock()`, then under lock set `rec.Status/rec.Result/rec.CompletedAt`. Do NOT hold `s.mu` across the LLM network call.)

- [ ] **Step 4: Thread the flag through api + mcp**

- `internal/api/api.go`: `GroupAPI.Submit(name string, summary bool)`; in the handler read `summary := r.URL.Query().Get("summary") == "true"` and pass it: `groups.Submit(name, summary)`.
- `internal/mcp/server.go`: `GroupAPI.Submit(name string, summary bool)`; `runGroupIn` gains `Summary bool \`json:"summary,omitempty" jsonschema:"also produce an LLM group summary"\``; handler calls `groups.Submit(in.Group, in.Summary)`.
- Update `api_test.go` / `mcp_test.go` fakes: `fakeGroupAPI.Submit(name string, summary bool)` + assert the flag is forwarded (e.g. a `?summary=true` request sets a captured bool).

- [ ] **Step 5: Run tests (race)**

Run: `go build ./... && go test ./internal/groupapi/ ./internal/api/ ./internal/mcp/ -race -v`
Expected: PASS. (`main.go` still calls `groupapi.New(groups, runner, timeout)` and never calls `Submit` directly, so it stays green; `Service.Summarizer` defaults nil until Task 4 wires it.)

- [ ] **Step 6: Commit** — SKIP.

---

## Task 3: Lark interactive path — "Run + summary" button + final reply

**Files:**
- Modify: `internal/core/types.go`, `internal/platform/lark/{decode.go, cards.go, groupreporter.go, dispatch.go}`
- Test: `internal/platform/lark/{decode_test.go, groupcards_test.go, groupreporter_test.go}`

**Interfaces:**
- Consumes: `summary.Client`, `summary.Summarize` (Task 1).
- Produces: `core.RunGroup` gains `Summary bool`; `groupReporter` gains `Results []core.ServiceResult`; `Adapter` gains `Summarizer summary.Client` + `SummaryMaxEvidenceBytes int`.

- [ ] **Step 1: Write the failing test**

```go
// internal/platform/lark/decode_test.go  (add)
func TestDecodeRunGroupWithSummary(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"run_group","cluster":"prod-1","group":"critical","summary":true}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil { t.Fatal(err) }
	rg, ok := in.(core.RunGroup)
	if !ok || !rg.Summary || rg.Name != "critical" {
		t.Fatalf("intent = %#v, want RunGroup{critical, Summary:true}", in)
	}
}

// internal/platform/lark/groupcards_test.go  (add)
func TestConfirmCardHasSummaryButton(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
	card := buildGroupConfirmCard(g)
	if !strings.Contains(card, "Run + summary") || !strings.Contains(card, "\"summary\":true") {
		t.Errorf("confirm card missing Run+summary button: %s", card)
	}
}

// internal/platform/lark/groupreporter_test.go  (add)
func TestGroupReporterAccumulatesResults(t *testing.T) {
	fs := &fakeGroupSender{}
	rep := newGroupReporter(fs)
	g := core.Group{Name: "g"}
	rep.Start(context.Background(), g, core.GroupDest{InReplyTo: "u"}, 1)
	rep.ServiceDone(context.Background(), g, core.ServiceResult{UseCase: "dt", Target: map[string]string{"deployment": "a"}})
	if len(rep.Results) != 1 || rep.Results[0].UseCase != "dt" {
		t.Fatalf("reporter did not accumulate results: %+v", rep.Results)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/lark/ -run 'RunGroupWithSummary|SummaryButton|AccumulatesResults' -v`
Expected: FAIL — `RunGroup.Summary`/`groupReporter.Results` undefined; no summary button.

- [ ] **Step 3: Implement**

- `internal/core/types.go`: add `Summary bool` to `RunGroup`.
- `internal/platform/lark/decode.go` `decodeCardAction`, `run_group` case: read the flag (JSON bool arrives as `bool` in `map[string]any`):
```go
	case "run_group":
		summary, _ := p.Action.Value["summary"].(bool)
		return core.RunGroup{Reply: reply, Name: group, Summary: summary}, nil
```
- `internal/platform/lark/cards.go` `buildGroupConfirmCard`: add a second button after the "Run ▸" button:
```go
	elements = append(elements,
		button2("Run ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name}),
		button2("Run + summary ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name, "summary": true}),
	)
```
- `internal/platform/lark/groupreporter.go`: add `Results []core.ServiceResult` to the `groupReporter` struct; in `ServiceDone`, append `r` to `gr.Results` (it's called serially — no lock). Add an exported-free accessor is unnecessary (same package).
- `internal/platform/lark/dispatch.go` `handleGroupRun`: add `Adapter.Summarizer summary.Client` + `Adapter.SummaryMaxEvidenceBytes int` fields (import `internal/summary`). In the goroutine, after `a.GroupRunner.Run(...)` returns nil AND `v.Summary` (or the group's config default `g.Summary`):
```go
		reporter := newGroupReporter(a.R.GroupSender())
		dest := core.GroupDest{InReplyTo: reply.MessageID}
		if err := a.GroupRunner.Run(bg, g, dest, reporter); err != nil {
			log.Printf("group run %s: %v", g.Name, err)
			return
		}
		if v.Summary || g.Summary {
			sum, warn := summary.Summarize(bg, a.Summarizer, g, reporter.Results, a.SummaryMaxEvidenceBytes)
			text := sum
			if warn != "" {
				text = "⚠️ " + warn
			}
			if _, e := a.R.GroupSender().ReplyID(bg, reply.MessageID, buildGroupSummaryCard(g, text)); e != nil {
				log.Printf("group summary reply %s: %v", g.Name, e)
			}
		}
```
Add `buildGroupSummaryCard(g core.Group, text string) string` to groupcards.go: a card titled `📋 **Group summary: <name>**` with `markdown(text)`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./internal/platform/lark/ ./internal/core/ -race -v`
Expected: PASS. (`Adapter.Summarizer` nil until Task 4 → a summary-requested interactive run posts the "not configured" warning card, which is correct.)

- [ ] **Step 5: Commit** — SKIP.

---

## Task 4: config + main + chart + openapi + verification

**Files:**
- Modify: `internal/config/config.go`, `internal/core/group.go`, `cmd/kato-bot/main.go`, `charts/kato-bot/{values.yaml, templates/secret.yaml, templates/deployment.yaml, templates/groups-configmap.yaml}`, `openapi.yaml`, spec doc, `README.md`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Config — GroupSummary + per-group default**

Add to config.go:
```go
// GroupSummaryConfig configures kato-bot's optional LLM group summarizer.
type GroupSummaryConfig struct {
	Enabled          bool
	BaseURL          string
	Model            string
	APIKey           string
	MaxTokens        int
	Temperature      float64
	Timeout          time.Duration
	MaxEvidenceBytes int
}
```
Add `GroupSummary GroupSummaryConfig` to `Config`. In `Load()`, parse (all optional; enabled defaults false):
```go
	if v, ok := os.LookupEnv("GROUP_SUMMARY_ENABLED"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("GROUP_SUMMARY_ENABLED: %w", err)
		}
		cfg.GroupSummary.Enabled = b
	}
	cfg.GroupSummary.BaseURL = envOr("GROUP_SUMMARY_BASE_URL", "https://api.openai.com/v1")
	cfg.GroupSummary.Model = os.Getenv("GROUP_SUMMARY_MODEL")
	cfg.GroupSummary.APIKey = os.Getenv("GROUP_SUMMARY_API_KEY")
	// GROUP_SUMMARY_MAX_TOKENS (int, optional), GROUP_SUMMARY_TEMPERATURE (float, optional),
	// GROUP_SUMMARY_TIMEOUT (duration, optional), GROUP_SUMMARY_MAX_EVIDENCE_BYTES (int, optional):
	// parse each with the existing strconv/ParseDuration guards, defaulting to 1024 / 0.2 / 0 / 16384.
```
Add a per-group `summary: true` default: `groupsFile` group entry gains `Summary bool \`yaml:"summary"\``; `GroupConfig` gains `Summary bool`; `loadGroups` copies it. Add `Group.Summary bool` to `core.Group` (core/group.go) and set it in main.go's flatten (`core.Group{..., Summary: gc.Summary}`).

Add a config test: `GROUP_SUMMARY_ENABLED=true` + model/key set → `cfg.GroupSummary.Enabled` and fields populated; a bad `GROUP_SUMMARY_ENABLED` errors; a group with `summary: true` → `GroupConfig.Summary` true.

- [ ] **Step 2: main.go — build + inject the summarizer**

```go
	var summarizer summary.Client
	if cfg.GroupSummary.Enabled {
		summarizer = &summary.OpenAIClient{
			BaseURL: cfg.GroupSummary.BaseURL, Model: cfg.GroupSummary.Model,
			APIKey: cfg.GroupSummary.APIKey, MaxTokens: cfg.GroupSummary.MaxTokens,
			Temperature: cfg.GroupSummary.Temperature, Timeout: cfg.GroupSummary.Timeout,
		}
	}
```
Set `gapi.Summarizer = summarizer` and `gapi.SummaryMaxEvidenceBytes = cfg.GroupSummary.MaxEvidenceBytes` (after `gapi := groupapi.New(...)`); add `Summarizer: summarizer, SummaryMaxEvidenceBytes: cfg.GroupSummary.MaxEvidenceBytes` to the `lark.Adapter{...}` literal. Import `internal/summary`.

- [ ] **Step 3: Chart**

- `values.yaml`: add a `groupSummary:` block near `lark:` —
```yaml
groupSummary:
  # -- Enable the optional LLM group-summary (kato-bot's only LLM use).
  enabled: false
  # -- OpenAI-compatible base URL.
  baseUrl: https://api.openai.com/v1
  # -- Model name.
  model: gpt-4o-mini
  # -- Max completion tokens.
  maxTokens: 1024
  # -- Sampling temperature.
  temperature: "0.2"
  # -- API key (rendered into the bot Secret as GROUP_SUMMARY_API_KEY). Ignored when lark.existingSecret is set — put the key in that Secret instead.
  apiKey: ""
```
- `templates/secret.yaml`: add `GROUP_SUMMARY_API_KEY: {{ .Values.groupSummary.apiKey | quote }}` to `stringData` (inside the existing `if not .Values.lark.existingSecret` guard).
- `templates/deployment.yaml`: add `env:` entries `GROUP_SUMMARY_ENABLED`, `GROUP_SUMMARY_BASE_URL`, `GROUP_SUMMARY_MODEL`, `GROUP_SUMMARY_MAX_TOKENS`, `GROUP_SUMMARY_TEMPERATURE` from `.Values.groupSummary.*` (the API key rides `envFrom.secretRef` already).
- `templates/groups-configmap.yaml`: emit `{{- if .summary }}\n        summary: true\n        {{- end }}` per group.

- [ ] **Step 4: openapi + spec + README**

- `openapi.yaml`: add `summary` (string) + `summaryWarning` (string) to `GroupResult`; add a `summary` boolean query parameter to `POST /api/v1/groups/{name}/run`. Update the run-view example to show a `summary` in `result`.
- Spec: mark Part 2 implemented (one line at top of Part 2).
- `make readme`.

- [ ] **Step 5: Full verification**

Run:
```
gofmt -l internal/ cmd/            # empty except pre-existing internal/core/registry_test.go
go build ./... && go vet ./... && go test ./... -race -count=1
helm template charts/kato-bot --set lark.appId=x --set lark.appSecret=y \
  --set groupSummary.enabled=true --set groupSummary.apiKey=sk-test | grep -iA2 GROUP_SUMMARY
```
Expected: all green; the rendered Secret has `GROUP_SUMMARY_API_KEY`, the Deployment has the `GROUP_SUMMARY_*` env; no stray root binary.

- [ ] **Step 6: Commit** — SKIP.

---

## Self-Review

- **Spec coverage (Part 2):** LLM client mirrored (Task 1); bounded prompt / non-healthy summaries / byte cap (Task 1); non-fatal nil-client + error → warning (Task 1); async JSON `summary`/`summaryWarning` + `?summary` (Task 2); MCP `summary` bool (Task 2); Lark "Run + summary" button + final reply + reporter results capture (Task 3); config `groupSummary` env + Secret + per-group default (Task 4); chart/openapi/docs (Task 4). Group-only opt-in (single-run flow untouched — no change to `pick`/`run`). Import boundary (summary/groupapi/api/mcp never import lark) held by injecting the client from main.
- **Placeholder scan:** the config float/int/duration parses in Task 4 Step 1 are described with their exact defaults + the existing guard to mirror (not full code, but unambiguous and pattern-identical to the shown `GROUP_SUMMARY_ENABLED` block); every other code step has real code. Test steps that reuse `newTestService`/`waitForStatus` name the exact helpers to copy. No `TBD`/`handle errors`.
- **Type consistency:** `summary.Client`/`OpenAIClient`/`Summarize(ctx, client, g, results, maxBytes)` defined in Task 1 and consumed identically in Tasks 2-4. `Service.Summarizer`/`SummaryMaxEvidenceBytes`, `Adapter.Summarizer`/`SummaryMaxEvidenceBytes`, `Submit(name, doSummary bool)`, `RunGroup.Summary`, `groupReporter.Results`, `GroupResult.Summary`/`SummaryWarning`, `Group.Summary`, `GroupConfig.Summary`, `GroupSummaryConfig` — each defined once and referenced consistently across tasks.
{% endraw %}
