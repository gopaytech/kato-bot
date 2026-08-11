{% raw %}
# kato-bot Group Runs — Multi-UseCase + Group Summary — Design

**Status:** Approved (design)

**Goal:** Two enhancements to the existing Group Runs feature
(`2026-08-07-group-runs-lark-design.md`):
1. **Multi-usecase groups** — a Group runs several UseCases, each with its own
   list of inputs, instead of a single UseCase over many targets.
2. **Group summary** — an optional, LLM-written narrative synthesizing the whole
   group run, produced by a new kato-bot LLM client.

Built in that order (two plans): multi-usecase first, then the summary on top of
stable group results.

**Builds on:** the shipped (uncommitted) Group Runs feature — async submit/poll
endpoint, `core.GroupRunner` bounded fan-out, `core.CollectingReporter`, the Lark
parent-card + threaded-reply flow, and the per-group in-flight gate. All of that
carries over unchanged except where noted.

---

# Part 1 — Multi-UseCase Groups

**Implemented** (2026-08-11-group-multi-usecase plan, Tasks 1-5): config
`usecases:` list, `core.Group.Items`/`WorkItem`, per-usecase Lark labels and
"N targets across M usecases" summaries, `ServiceView.usecase`, and the nested
chart render are all shipped; the deprecated `usecase:`/`targets:` shape and
the `WorkItems()` fallback have been removed.

## Problem

Today a Group is `{cluster, usecase, targets:[inputs]}` — one UseCase fanned out
over many input-sets. Operators want one Group to cover several UseCases at once
(e.g. `deployment-troubleshooting` on a set of deployments **and**
`http-connectivity-check` on a set of endpoints). Because different UseCases have
different input contracts, a cartesian "usecases × targets" matrix is invalid;
each UseCase must pair with its own inputs.

## Config shape (replaces the single-usecase shape)

A Group carries a `usecases:` list; each entry is one UseCase + its own targets.
The old top-level `usecase:` + `targets:` is **removed** (the feature is
unreleased, so no backward-compat is kept).

```yaml
groups:
  - name: critical-services         # unique
    cluster: prod-1                  # must exist in the cluster registry
    concurrency: 5                   # optional; group-level, one worker pool
    usecases:                        # >= 1 entry
      - usecase: deployment-troubleshooting
        targets:                     # >= 1 per usecase; each an arbitrary inputs map
          - { namespace: payments, deployment: payment-api }
          - { namespace: cart,     deployment: cart-api }
      - usecase: http-connectivity-check
        targets:
          - { target: api.internal, port: "443", scheme: https, path: /healthz }
```

**Validation** (startup, fail-fast): unique group name; `cluster` non-empty and
resolves in the registry (checked in main.go); **≥1 usecase**; each `usecase`
name non-empty; **each usecase has ≥1 target**; each target a non-empty map;
`concurrency` defaulted (5) and clamped.

## Core model — flatten to a work list

`config.GroupConfig` gains `UseCases []GroupUseCase{ UseCase string; Targets
[]map[string]string }` and drops `UseCase`/`Targets`.

At startup, main.go **flattens** the nested config into a flat work list on
`core.Group`:

```go
type Group struct {
    Name        string
    Cluster     string
    Concurrency int
    Items       []WorkItem   // replaces UseCase + Targets
}
type WorkItem struct {
    UseCase string
    Inputs  map[string]string
}
```

The nesting is an authoring convenience only; the runner sees a flat `[]WorkItem`.
`core.ServiceResult` gains `UseCase string` so every result records which UseCase
produced it.

## Runner — essentially unchanged

`GroupRunner.Run` fans out over `g.Items` instead of `(g.UseCase × g.Targets)`;
each job carries its item's `UseCase` + `Inputs`, and `runOne(kc, item.UseCase,
item.Inputs)` (which already takes a usecase param) is unchanged. Same bounded
worker pool sized by group `concurrency`, same retry, same serialized
`GroupReporter` contract, same per-group in-flight gate (`TryAcquire`), same
4-way tallies. `total = len(g.Items)`.

## Reporting — label by usecase

- **JSON:** `ServiceView` gains `"usecase"`. `GET /api/v1/groups` list view
  becomes `{name, cluster, usecases:[{usecase, targets:<count>}], totalTargets}`.
- **Lark:** the per-service threaded reply is labeled
  `🔴 deployment-troubleshooting · payments/payment-api — <headline>`; the parent
  card shows `N targets across M usecases` + the same 4-way tallies; the confirm
  card and picker Groups section list the usecases.

## Non-goals (Part 1)

One cluster per group (unchanged); no cartesian expansion; concurrency stays
group-level (one pool across all usecases), not per-usecase.

## Files touched (Part 1)

`internal/config/config.go` (schema + validation), `internal/core/group.go`
(`Group.Items`/`WorkItem`, `ServiceResult.UseCase`, runner feeder),
`cmd/kato-bot/main.go` (flatten), `internal/platform/lark/{groupcards.go,
cards.go}` (labels), `internal/groupapi/groupapi.go` (`ServiceView.usecase`,
`GroupResult`, list view), `openapi.yaml`, `charts/kato-bot/templates/
groups-configmap.yaml` + `values.yaml` example, the spec, and all tests.

---

# Part 2 — Group Summary (LLM)

**Implemented** (2026-08-11): `internal/summary`, `groupapi.Service`/`lark.Adapter`
`Summarizer` + `SummaryMaxEvidenceBytes`, `config.GroupSummaryConfig` +
per-group `summary` default, chart, and `openapi.yaml` — see
`.superpowers/sdd/2026-08-11-group-summary/`.

## Problem

Each service already gets kato's per-run summary. Operators want one **group-level
narrative** synthesizing the whole run ("18/20 healthy; the 2 failures are X and
Y, both in payments — likely a bad deploy; do Z"). That synthesis needs an LLM,
and kato can't do it (it has no concept of a group). So kato-bot gains its **first
LLM capability**, used only for this — a deliberate exception to the "no LLM in
kato-bot" principle.

## kato-bot LLM client

A small OpenAI-compatible client (mirrors kato's `summarizer.OpenAIClient`):
`{ BaseURL, Model, APIKey, MaxTokens, Temperature, Timeout }` with a
`Summarize(ctx, system, user) (string, error)` call. Lives in a new
`internal/summary/` package (platform-agnostic; no Lark import).

**Config** via a Helm `groupSummary:` block → env, API key from a Secret (same
pattern as the Lark secret):

```yaml
groupSummary:
  enabled: true
  baseUrl: https://api.openai.com/v1
  model: gpt-4o-mini
  maxTokens: 1024
  temperature: "0.2"
  apiKeySecretRef:          # existing Secret + key of your choosing for the LLM API key
    name: alicloud-model-key
    key: apiKey
  # apiKey: ""              # or inline → chart renders its own <name>-groupsummary Secret
```

Env: `GROUP_SUMMARY_ENABLED`, `GROUP_SUMMARY_BASE_URL`, `GROUP_SUMMARY_MODEL`,
`GROUP_SUMMARY_API_KEY`, `GROUP_SUMMARY_MAX_TOKENS`, `GROUP_SUMMARY_TEMPERATURE`.
When not enabled/configured, summaries are unavailable: a requested summary yields
an empty `summary` + a clear `summaryWarning` ("group summary not configured"),
and the run still succeeds.

## Opt-in (group-only)

The summary is a **group** concept; the single-usecase run flow is untouched.

- **Lark:** the group **confirm card offers two buttons — "Run" and "Run +
  summary."** Off by default; the user opts in per run. A `run_group` action
  carries a `summary: true` flag when the second button is used.
- **Endpoint/MCP:** `POST /api/v1/groups/{name}/run?summary=true` (query param —
  the POST has no body today); the `run_group` MCP tool takes a `summary` bool.
- **Group config default:** a group may set `summary: true` to default it on;
  the per-run flag overrides.

## Synthesis

After all services complete (the `CollectingReporter` holds every result), and
only when summary is requested, build a **bounded** prompt:

- For **every** service: `usecase · target · bucket · headline` (one line).
- For **non-healthy** services (unhealthy/errored/unknown): also the truncated
  per-service `summary` (head+tail), capped to a total evidence budget
  (`GROUP_SUMMARY_MAX_EVIDENCE_BYTES`, default ~16 KB) so a 100-service group
  can't blow the context.
- System prompt: *"You are a Kubernetes SRE. Given these per-service
  troubleshooting results, write a concise group health summary: overall status,
  the notable failures, any common theme, and the suggested next action. Use only
  this evidence; do not invent data."*

One LLM call → the group narrative string.

## Output

- **Lark:** a final **"📋 Group summary"** threaded reply under the parent card,
  posted after the per-service replies (and after the final tally patch).
- **JSON (async):** a `summary` string field on `GroupResult` (and thus in the
  `RunView.result` returned by the poll endpoint), populated when requested and
  the run is `done`; plus `summaryWarning` when it couldn't be produced.

## Async interaction

For the endpoint/MCP path, the summary is produced at the **end of the background
run** (after the fan-out completes), then stored on the run record alongside the
results — so a poll after `status: done` includes it. The summary call is bounded
by its own client `Timeout` and shares the run's overall `GROUP_RUN_TIMEOUT`
budget.

## Failure = non-fatal

If the LLM call fails (down, timeout, bad key) or summary isn't configured, the
group **results are still returned**; `summary` is empty and `summaryWarning`
carries the reason. Never aborts the run — same contract as kato's own
summary-failure → `Warning`.

## Files touched (Part 2)

New `internal/summary/` (OpenAI client + prompt builder); `internal/config`
(groupSummary env + validation); `internal/groupapi` (accept `summary` flag,
call the summarizer at end-of-run, store `summary`/`summaryWarning` on the
record + `GroupResult`); `internal/api` + `internal/mcp` (thread the `summary`
flag); `internal/core` (a small `GroupResults` accessor if needed for the prompt
builder — or build the prompt from `CollectingReporter.Results`);
`internal/platform/lark/{cards.go, dispatch.go, decode.go}` (the "Run + summary"
button, `run_group` summary flag, final summary reply); `cmd/kato-bot/main.go`
(build the summarizer from config, inject into groupapi + adapter);
`charts/kato-bot` (`groupSummary` values + Secret key + deployment env);
`openapi.yaml`; the spec; and all tests.

## Non-goals (Part 2)

No streaming of the summary; no multi-model routing (one configured model); no
persistence beyond the existing ephemeral in-memory run store; the summary never
influences tallies or per-service verdicts (advisory, like kato's).
{% endraw %}
