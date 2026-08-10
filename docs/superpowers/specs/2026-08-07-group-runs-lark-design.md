{% raw %}
# kato-bot Group Runs + Native Lark Reporting — Design

**Status:** Approved (design)

**Goal:** Run one UseCase across a predefined static list of single-cluster
targets ("a Group"), on demand, and report the results into Lark **exactly like a
single run** — same cards, same summaries — just fanned out. A Group is one tap in
the existing Lark flow, or a single kick-off endpoint for a scheduler. All
fan-out, backpressure, and Lark posting happen inside kato-bot; no second
reporting path.

**Depends on:** kato's run health verdict
(`kato/docs/superpowers/specs/2026-08-07-run-health-verdict-design.md`) for the
🟢/🔴 tallies. Loosely coupled — with no verdict, services fall into the ❔
"unknown" bucket and everything else works — so the two ship independently.

## Problem

Teams want to check ~100 critical services with a UseCase (e.g.
`deployment-troubleshooting`) and see the results in Lark. Driving that from an
external tool (n8n → Lark) means a **second, inconsistent reporting path**: it
wouldn't look or behave like the kato-bot cards users already know. kato is
deliberately on-demand (not a monitor), and a single kato `/run` is short, so a
"batch" is really *100 short runs paced with backpressure*. kato-bot already
owns the Lark experience and per-cluster fan-out — the Group belongs here.

## Non-goals

- **A continuous loop / scheduler inside kato-bot.** Cadence is owned by whatever
  hits the kick-off endpoint (n8n cron, a k8s CronJob). kato-bot runs a Group
  once per trigger.
- **Dynamic target discovery (label selectors).** Targets are a static, operator-
  maintained list. No in-cluster querying.
- **Cross-cluster Groups.** One Group targets one cluster. (Multi-cluster Groups
  are a future extension; the schema leaves room but the runner assumes one
  `KatoClient`.)
- **Persisting Group runs in kato-bot.** kato-bot stays stateless, single-replica,
  no DB. A Group run is ephemeral in-memory state (like today's async single
  run). Per-service audit already exists: each service is a real kato `/run` and
  persists a `Run` CR.
- **A `GroupUseCase` CRD.** Rejected in brainstorming — the static list lives in
  config; a CRD/watch/RBAC is unjustified.
- **Changing the single-run experience.** The pick-cluster → pick-usecase → form
  → run path is untouched, no extra taps.

## Group definition (config)

Loaded like `clusters`, from a file (`KATO_GROUPS_FILE`, default
`/etc/kato-bot/groups.yaml`), rendered from Helm `groups:` into the kato-bot
ConfigMap (a second key alongside `clusters.yaml`; the existing checksum
annotation rolls pods on change).

```yaml
groups:
  - name: critical-services         # unique
    cluster: prod-1                  # must exist in the cluster registry
    usecase: deployment-troubleshooting
    concurrency: 5                   # optional; default 5, ceiling e.g. 10
    targets:                         # >= 1; each an arbitrary inputs map
      - { namespace: payments, deployment: payment-api }
      - { namespace: cart,     deployment: cart-api }
      # ... up to ~100
```

`targets` are arbitrary input-maps, so a Group works for **any** UseCase, not just
deployment-troubleshooting. Validated at startup: unique names; `cluster` resolves
in the registry; `usecase` non-empty; ≥1 target; each target a non-empty map;
concurrency defaulted and clamped. Invalid config fails fast, like clusters.

## Architecture

```
internal/config/      + Group type, groups-file load + validation (KATO_GROUPS_FILE)

internal/core/        platform-agnostic (no Lark import):
                      · Group, GroupRun, ServiceResult, GroupSummary types
                      · GroupReporter port  { StartGroup, ServiceDone, Finish }
                      · Groups registry (name → Group, insertion-ordered)
                      · GroupRunner: bounded fan-out to a cluster's KatoClient.Run,
                        retry-on-429/5xx, verdict → bucket, emits via GroupReporter
                      · new intents PickGroup, RunGroup + core.Handle cases

internal/platform/lark/  the one GroupReporter implementation:
                      · picker gains a Groups section; confirm card; parent
                        progress card (reply) + patch; threaded per-service reply
                      · decode: pick_group, run_group actions

internal/api/         + GET /api/v1/groups, POST /api/v1/groups/{name}/run (202)
internal/mcp/         + tools list_groups, run_group
cmd/kato-bot/         wire a GroupService (GroupRunner + Lark GroupReporter +
                      Groups registry) into the Lark adapter and the api/mcp servers
```

### GroupRunner (core)

Given a resolved Group and its cluster `KatoClient`, fan out to `Run(usecase,
target)` with a **bounded worker pool** sized by the Group's `concurrency`, under
the existing global `MAX_CONCURRENT_RUNS` semaphore (protects total in-flight
across single + group runs). Each target maps to a `ServiceResult`:

- kato returned a run → carry `Healthy *bool`, `Headline`, `Summary`, `Phase`.
- kato errored (429/5xx/timeout) after bounded retries with backoff →
  `Err` set. This is the **errored** bucket, distinct from *unhealthy*.

Results are pushed to the injected `GroupReporter` as they complete
(`ServiceDone`), then a final `GroupSummary` (`Finish`). The runner is async: like
`SubmitForm` today it returns a deferred thunk the adapter runs in a goroutine
after fast-ack.

### Four-way buckets

Every service lands in exactly one: 🟢 **healthy** (`Healthy==true`) · 🔴
**unhealthy** (`Healthy==false`) · ⚠️ **errored** (the check failed to run) · ❔
**unknown** (`Healthy==nil`, e.g. kato has no verdict yet). The parent card shows
all four counts; the runner never miscolors an errored/unknown service as green.

### Lark reporting (GroupReporter impl)

Reuses existing `sender` reply/patch primitives:

- `StartGroup` → post a **parent card** (Group, usecase, cluster, N targets, a
  progress line, zeroed tallies) as a reply; keep its message id.
- `ServiceDone` → post a **threaded reply** carrying that service's full kato
  summary (same rendering as a single run's result), and **patch** the parent
  card's progress + tallies.
- `Finish` → patch the parent card to the final rollup.

This yields the approved "parent card + threaded replies" shape without flooding
the channel.

## Lark flow impact (additive)

Current path is unchanged. Groups hook into **one existing card** — the usecase
picker shown after picking a cluster (`RenderPicker`) — which gains a **Groups
section** listing the Groups whose `cluster` matches the chosen one.

```
message               → ListClusters  → cluster picker            (unchanged)
[pick_cluster]        → PickCluster    → usecase picker + GROUPS   (picker gains a section)
  click a UseCase [pick]      → PickUseCase → form → [run] → result   (UNCHANGED, no extra taps)
  click a Group [pick_group]  → PickGroup   → confirm card             (NEW: no form; targets are fixed)
    confirm     [run_group]   → RunGroup    → parent card + fan-out    (NEW)
```

`decode.go` gains two cases (`pick_group`, `run_group`); the existing
`pick_cluster` / `pick` / `run` cases are untouched. A Group pins its own cluster
in config (the source of truth for the endpoint path, which has no interactive
pick); the picker merely filters Groups to the chosen cluster, so one mental model
holds across both entry points.

## Kick-off endpoint + MCP (scheduled path)

Group runs can take 5-10 minutes for ~100 targets — long enough to die behind
proxies and client timeouts if held open. So this path is **submit + poll**,
not synchronous request/response:

- `GET /api/v1/groups` → list configured Groups (name, cluster, usecase, target
  count).
- `POST /api/v1/groups/{name}/run` → **submits** the Group and returns
  **202** immediately with `{runId, group, status: "running"}`. The run
  executes on a background goroutine bound to `context.Background()` (not the
  submitting request's context) — a client disconnect, proxy timeout, or
  n8n/CronJob giving up on the HTTP call can never abort the run.
  `core.CollectingReporter` accumulates results instead of rendering anything;
  this path never posts to Lark. **404** for an unknown Group; **409** if that
  Group is already running (a per-group in-flight guard, shared with the
  interactive Lark path via `GroupRunner.TryAcquire`, so the two entry points
  can't overlap).
- `GET /api/v1/groups/runs/{runId}` → **polls** a submitted run: `status`
  (`running`/`done`/`failed`), and once terminal, `result` (the same
  `GroupResult` — group identity, tallies, per-service results) or `error`.
  **404** for an unknown/expired runId.
- MCP tools mirror this: `run_group` submits and returns `{runId, status}`;
  `get_group_run` polls by `run_id` and returns the same JSON as its text
  result; `list_groups` mirrors `GET /api/v1/groups`.

Runs live in an **in-memory, ephemeral, bounded store** on the `groupapi.Service`
(mutex-guarded; capped at 256 records with a 1-hour TTL for finished runs,
evicted lazily on submit — a running record is never evicted). Like today's
single-run path, this is single-replica, no DB: a kato-bot restart loses
in-flight and recently-finished run records, and the caller must re-submit.
The `GroupResult` JSON shape is unchanged from before — only the delivery
changed, from one blocking response to submit-then-poll.

n8n/CronJob is reduced to a trigger: it POSTs, gets a runId back immediately,
and polls until the run is done — kato-bot does all fan-out and holds the
result, but posting it anywhere (Lark, a dashboard, wherever) is the caller's
job. The interactive Lark card flow (pick a Group from the picker) is
unaffected and still posts progress into the chat that triggered it.

## Error handling

| Situation | Result |
|---|---|
| A service's kato call fails after retries | ⚠️ errored bucket; its reply says "check failed to run"; group continues |
| A service run reports unhealthy | 🔴 bucket, full summary in its threaded reply |
| kato returns no verdict (older kato) | ❔ unknown bucket; not colored green |
| Lark patch/reply fails | logged, skipped; never aborts the group |
| Group not found (endpoint) | 404 with a clear message |
| Group already running (endpoint) | 409 with a clear message |
| kato-bot restarts mid-group | in-flight group lost (ephemeral, single-replica); re-trigger. Documented limitation — matches today's async single run |
| Global concurrency saturated | kato returns 429; runner backs off/retries per service |

## Testing

Table-driven, no network (matches existing package tests):

- **config:** groups-file parse; validation (dup names, unknown cluster, empty
  usecase/targets, concurrency clamp).
- **GroupRunner** (fake `KatoClient` + fake `GroupReporter`): concurrency cap
  honored; retry-on-429 then success vs. errored bucket; verdict → correct bucket
  (healthy/unhealthy/unknown); empty/duplicate targets; reporter receives
  `StartGroup` → `ServiceDone`×N → `Finish` with correct tallies.
- **Lark cards** (JSON assertions, existing style): picker *with* a Groups
  section; confirm card; parent card; threaded reply; tally patch.
- **decode:** `pick_group` → `PickGroup`, `run_group` → `RunGroup`; cluster
  threaded through.
- **api/mcp:** `GET /groups`; `POST /groups/{name}/run` → 202 with `{runId,
  group, status}` / 404 (unknown) / 409 (already running); `GET
  /groups/runs/{runId}` → 200 with the polled `RunView` (tallies +
  per-service views once done) / 404 (unknown runId); MCP `list_groups` /
  `run_group` / `get_group_run`.
- **groupapi.Service:** submit-then-poll async behavior (status `running`
  immediately after submit, `done`/`failed` once the background goroutine
  finishes, proven via a blocking fake `KatoClient` + bounded poll loop, not
  wall-clock sleeps); the background run is bound to `context.Background()`,
  not the submitting request's context; the in-memory store's eviction
  (cap + TTL, never evicts a running record).

## Files touched

`internal/config/config.go`; `internal/core/{types.go, registry.go, core.go}` +
new `internal/core/group.go`; `internal/platform/lark/{decode.go, cards.go,
render.go, sender.go, cardaction.go}`; `internal/api/api.go`;
`internal/mcp/server.go`; `cmd/kato-bot/main.go`; `charts/kato-bot`
(values `groups`, ConfigMap `groups.yaml` key, deployment mount) + README regen;
`openapi.yaml` (root: `/groups` endpoints); tests alongside each.
{% endraw %}
