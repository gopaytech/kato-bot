# kato-bot Architecture

kato-bot is a **chat adapter** for [kato](https://github.com/gopaytech/kato), the
Kubernetes troubleshooting operator, currently implemented for **Lark (Feishu)** and
**Telegram**. It lets a chat user pick a cluster, pick a troubleshooting UseCase, fill
in that UseCase's declared inputs on an interactive card (Lark) or through a short
question-by-question wizard (Telegram), and get kato's LLM-written summary back — Lark
morphs a single card in place, Telegram edits its own message in place to the same
effect.

It is deliberately a **thin adapter**: all troubleshooting logic (which checks run, in
what order, what the LLM sees) lives in kato. kato-bot only discovers UseCases, collects
inputs, triggers runs, and renders results.

```
Lark cloud ◄──ws (dial-out)── kato-bot ──REST──► kato (cluster A)
                                        ──REST──► kato (cluster B)
                                        ──REST──► ...
```

- **Single binary, single replica, no ingress.** The bot dials *out* to Lark over a
  WebSocket long-connection, long-polls Telegram's `getUpdates`, and reaches each kato
  over plain REST. The only listener is a `/healthz` + `/readyz` probe server for
  Kubernetes.
- **Near-stateless.** No database. Most flow state rides inside the message itself
  (Lark's card `value` payloads, or the cluster/UseCase riding in Telegram's
  `callback_data`); Telegram's input wizard is the one exception, holding pending
  answers in a small in-memory, TTL'd session map. kato persists every run as a `Run`
  CRD, so audit history lives there, not here.
- **No auth of its own.** kato's API is unauthenticated (and read-only by design);
  access to the bot — and therefore to kato — is governed entirely by chat group/DM
  membership on whichever platform(s) are configured.

## The kato contract (what this bot depends on)

kato is a combined Kubernetes operator + HTTP server. kato-bot uses exactly three
endpoints of its REST API (`internal/kato/client.go`):

| Endpoint | Used for |
|---|---|
| `GET /api/v1/usecases` | list UseCases → `{usecases:[{name,description,inputs,ready}]}` |
| `GET /api/v1/usecases/{name}` | one UseCase's input contract |
| `POST /api/v1/usecases/{name}/run?includeOutputs=false` | execute; body `{"inputs":{...}}` → `{run,phase,summary,warning}` |

Facts about kato that shape this design:

- **`POST /run` is synchronous and slow.** kato executes every step *and* waits for the
  LLM summary before responding — tens of seconds, up to minutes. The bot must never
  block a Lark callback on it (see [Fast-ack contract](#fast-ack--async-result)).
  `KATO_RUN_TIMEOUT` (default `360s`) bounds the client side of this call.
- **kato validates inputs.** All input values are strings on the wire. Missing/unknown
  inputs → `400` with a message; a UseCase that failed validation is `ready:false` and
  `/run` returns `422`; kato's own concurrency cap returns `429`. The bot maps each of
  these to friendly card text (`core.friendlyKatoError`).
- **No auth, no polling.** kato has no API keys and no async run-status endpoint; the
  synchronous response is the whole result. The bot requests `includeOutputs=false`
  because v1 shows the summary, not raw per-step outputs.
- **kato is single-cluster.** One kato serves the cluster it runs in. Multi-cluster
  support is therefore a *bot-side* concern: one kato URL per cluster (below).

## Ports-and-adapters layout

The core principle: `internal/core` owns all orchestration and imports **no platform
packages**. Platforms (Lark and Telegram today) are adapters that only decode events
into intents and render semantic states into cards or messages.

```
cmd/kato-bot/main.go       wiring: config → kato clients → registry → core → adapters
                           (Lark and/or Telegram, whichever are configured)

internal/config/           env config + clusters YAML file loading/validation

internal/core/             platform-agnostic types + state machine
  types.go                 ports: KatoClient, Renderer, Intent, Reply, HTTPStatusError
  registry.go              Registry: cluster name → KatoClient (built once, read-only)
  core.go                  Core.Handle: intent → kato calls → renders; error mapping

internal/kato/             REST client implementing core.KatoClient; maps non-2xx to
                           *APIError (carries HTTP status + kato's {"error":...} detail)

internal/platform/         platform.Adapter{Name, Start} + platform.Deps: the seam
  platform.go              main.go wires against; per-platform New(cfg, deps) returns
                           (nil, nil) when that platform's config is absent

internal/platform/lark/    Lark adapter
  dispatch.go              larkws long-connection, event handlers, dedup, run semaphore
  decode.go                event JSON → core.Intent (pure, SDK-free)
  cards.go                 card builders (pure functions → Card JSON 2.0 strings)
  cardaction.go            card-action handling: captureRenderer + async run + patch
  render.go                Renderer: emit = patch existing card | reply with new card
  sender.go                larkim API calls: message reply + message patch

internal/platform/telegram/  Telegram adapter
  dispatch.go               getUpdates long-poll loop, dedup, run semaphore
  decode.go                 update JSON → core.Intent (pure, SDK-free)
  callback.go               names-based callback_data encode/decode
  session.go                in-memory TTL'd wizard session store
  render.go                 Renderer: emit = edit existing message | send new one
  groupreporter.go          core.GroupReporter: edit-in-place group progress

charts/kato-bot/           Helm chart: 1-replica Deployment, Secret(s), clusters ConfigMap
```

### The ports (`internal/core/types.go`)

- **`KatoClient`** (inbound dependency): `ListUseCases` / `GetUseCase` / `Run`.
  Implemented by `internal/kato`. One client instance per configured cluster.
- **`Renderer`** (outbound port): `RenderClusterPicker` / `RenderPicker` / `RenderForm`
  / `RenderRunning` / `RenderResult` / `RenderError`. Implemented by both the Lark and
  Telegram adapters.
- **`Intent`** (inbound events): `ListClusters`, `PickCluster`, `PickUseCase`,
  `SubmitForm`. Produced by each adapter's own decoder.
- **`Reply`** — the opaque addressing context threaded through everything, shared by
  both adapters (Telegram's numeric chat/message ids stringified into the same
  fields): `ChatID`, `MessageID` (the bot's own message id, for patching/editing),
  `InReplyTo` (the user's message, for the first reply), and `Cluster` (the selected
  cluster name).
- **`HTTPStatusError`** — a tiny interface (`HTTPStatus() int`, `Detail() string`) that
  lets core map kato failures to status-specific friendly text without importing the
  kato package (which imports core; importing back would cycle).

### The state machine (`core.Core.Handle`)

`Handle` is fully synchronous and returns `(deferred, err)`. `deferred` is non-nil
**only** for a validated `SubmitForm` — it is a thunk containing the slow
`kato.Run → RenderResult` work, which the adapter runs in a goroutine. This split keeps
core unit-testable (tests call `deferred` directly) while the adapter owns goroutines
and the platform's fast-ack requirement.

| Intent | Core does | Card shown |
|---|---|---|
| `ListClusters` | `Registry.List()` | cluster picker |
| `PickCluster` | resolve client → `ListUseCases` | UseCase picker |
| `PickUseCase` | `GetUseCase` | input form |
| `SubmitForm` (missing required) | re-render form | form + "required: …" banner |
| `SubmitForm` (valid) | `RenderRunning`, return `deferred` | "⏳ running…", later patched to result |

Error mapping (`friendlyKatoError`): `400` → "invalid inputs: …" (on a run, re-rendered
*as a form error* so the user can fix and resubmit), `404` → "use case not found",
`422` → "failed validation in the cluster", `429` → "kato is busy", `5xx` → "internal
error", transport/timeout → "couldn't reach kato". An unknown cluster name on a stale
card (config changed since the card was posted) → "unknown cluster … — start over".

## Multi-cluster

One central bot targets many clusters (topology "T1" — see
`docs/superpowers/specs/2026-06-17-multitenancy-design.md`). The deciding constraint:
Lark's long-connection mode delivers each event to **one random connection** among all
instances sharing an app id, with no content-based routing — so "one bot instance per
cluster" cannot work. Instead:

- Clusters are configured as a YAML list (`KATO_CLUSTERS_FILE`, rendered from the Helm
  `clusters:` value into a ConfigMap): `name` (unique, required), `url` (required),
  optional `label` (picker button text) and `insecureSkipVerify` (skip TLS verification
  for a self-signed https kato URL — trusted networks only).
- `main.go` builds one `kato.Client` per cluster into a `core.Registry` (insertion
  ordered, immutable after startup).
- The chosen cluster **rides inside every card button's `value`** (like `usecase`
  does), is decoded into `Reply.Cluster`, and resolves the right client on each intent.
  No server-side session state; the picker is always shown, even with one cluster.
- Reachability from the bot to every cluster's kato URL (peering, a management cluster,
  or per-cluster exposure) is the operator's responsibility.

## The Lark adapter

### Event flow

Two event types arrive over the `larkws` long-connection (`dispatch.go`):

1. **`im.message.receive_v1`** — any DM, or a group message where the bot is
   @mentioned (`shouldRespond`), maps to `ListClusters`. The handler returns
   *immediately* (so Lark ACKs) and does the real work — a kato call plus a Lark
   reply — in a goroutine with a fresh 30s context. The first card is posted with
   `im.v1.message.reply` with `ReplyInThread(true)`, so the whole flow lives in a
   thread off the user's message and keeps the channel tidy.
2. **`card.action.trigger`** — a button click or form submit. The SDK's typed event is
   re-marshalled into a small JSON shape and decoded by the pure, SDK-free
   `decodeCardAction` into `PickCluster` / `PickUseCase` / `SubmitForm` based on
   `action.value.action` (`pick_cluster` / `pick` / `run`); form field values arrive
   in `action.form_value`.

### Two render paths

The same `core.Renderer` interface is implemented twice, on purpose:

- **`captureRenderer`** (`cardaction.go`) — for card actions. It *builds* the card
  string instead of sending it, and `handleCardAction` returns that card **in the
  callback response** (`type:"raw"`). Lark updates the clicked card inline with zero
  extra API calls, and the card stays interactive.
- **`Renderer` + `apiSender`** (`render.go`, `sender.go`) — for everything that can't
  ride a callback response: the initial picker (a *reply* to the user's message) and
  the async run result (a *patch* of the existing card via `im.v1.message.patch`,
  addressed by the card's own `open_message_id` from the callback context).

`emit` picks between them mechanically: `MessageID` set → patch, else reply.

### Cards (`cards.go`)

All cards are **Card JSON 2.0** (`"schema":"2.0"`). This is load-bearing twice over:
the `form`/`input` components only exist in 2.0 (in the legacy schema the submit button
silently fires no callback), and Lark rejects patching a card with a different-version
card — so the entire flow must be one schema. Builders are pure `map[string]any` →
JSON-string functions, asserted directly in tests. Forms use
`form_action_type:"submit"` + a `behaviors` callback; a UseCase with zero inputs skips
the form container and uses a plain callback button. Every card after the cluster pick
carries a shared context block ("☰ Cluster: … / ☰ Inputs: …") so a finished thread is
self-describing.

### Fast-ack + async result

The full lifecycle of a form submit:

```
click ▶ Run
  └─ handleCardAction (sync, within Lark's callback window)
       ├─ Core.Handle validates inputs
       │    ├─ missing → returns form card w/ error   (callback response)
       │    └─ valid   → "running…" card              (callback response)
       │                 + non-nil deferred
       ├─ try semaphore (cap MAX_CONCURRENT_RUNS, default 4)
       │    └─ full → "kato is busy" card instead     (callback response)
       └─ go: deferred(ctx w/ KATO_RUN_TIMEOUT)       (async)
            ├─ kato POST /run … blocks tens of seconds
            └─ PatchCard(result) with a FRESH 15s context
```

Details that matter:

- The semaphore is acquired **before** returning the "running" card, so an over-cap
  submit honestly shows "busy" rather than a running card that never resolves.
- The result patch uses a fresh context because a slow run can consume the entire run
  budget — the result (even an error) must still reach the card.
- A kato `400` on the run re-renders the **form** (prefilled, with the error banner)
  rather than a dead-end error card, so the user can correct and resubmit.

### At-least-once delivery

Lark redelivers an event that isn't ACKed quickly. Two defenses:

- Message handlers return immediately and do work in goroutines (fast ACK).
- A bounded FIFO dedup set (`dedup`, 1024 ids) drops redelivered message events, so one
  "@kato start" can't produce two picker cards. Card actions don't need dedup: their
  handling is idempotent repainting of the same card.

## The Telegram adapter

Telegram has no card component, so the adapter reaches the same UX by other means:

- **Transport is `getUpdates` long-polling** — pull instead of dial-out, but the same
  "no ingress" shape as Lark's WebSocket.
- **Message-edit stands in for card-patch**: `Renderer.emit` sends a new message when
  `Reply.MessageID` is empty and edits that message thereafter, so it morphs through
  picker → form → running → result like a Lark card does.
- **A conversational wizard replaces the form widget**: once a UseCase is picked, the
  bot asks for each declared input one message at a time (`/cancel` aborts). State
  lives in an in-memory, TTL'd `sessions` store keyed by `(chat, message)`, with a
  `(chat, user)` reverse index so a plain-text reply resolves back to the wizard.
- **`callback_data` is short names, not JSON** (`c|cluster`, `u|cluster|usecase`, …) to
  fit Telegram's 64-byte limit.
- **`platform.Adapter`** (`Name`/`Start`) plus a per-platform `New(cfg, deps)` that
  returns `(nil, nil)` when unconfigured is the seam `main.go` wires against — Lark
  and Telegram run in the same process, each optional.
- **Groups** reuse `core.GroupRunner`; the `GroupReporter` posts one progress message
  and edits it in place as targets finish, mirroring Lark's edit-in-place card.

## Configuration

Everything comes from the environment (`internal/config`); the Helm chart sets these on
the Deployment. At least one platform must be configured: `LARK_APP_ID` /
`LARK_APP_SECRET` for Lark, or `TELEGRAM_BOT_TOKEN` for Telegram (either from a
chart-managed or pre-existing Secret) — both may be set to run side by side. The
clusters file must exist and contain ≥1 valid cluster — misconfiguration fails fast at
startup. Other knobs: `LARK_BASE_URL` (Lark international vs Feishu China),
`TELEGRAM_API_BASE_URL`/`TELEGRAM_POLL_TIMEOUT`, `KATO_RUN_TIMEOUT`,
`MAX_CONCURRENT_RUNS`, `LOG_LEVEL`, `HEALTH_ADDR`. See the README for the full table.

## Deployment

The Helm chart (`charts/kato-bot/`) deploys:

- a **single-replica** Deployment — required, not just simple: state rides in cards/
  messages but Lark's WebSocket event routing is random across connections, and a
  second `getUpdates` poller would race Telegram's offset with the first; the run
  semaphore is also per-process. No Service or Ingress exists; the only inbound
  surface is the probe port.
- a ConfigMap holding `clusters.yaml`, mounted at `/etc/kato-bot/` and annotated with
  its own checksum so cluster changes roll the pod.
- a Secret per configured platform — Lark credentials (or `lark.existingSecret`) and/or
  the Telegram bot token (or `telegram.existingSecret`).

A probe-port bind failure is deliberately fatal at startup: otherwise the bot would run
while Kubernetes kills the pod on failing probes with no obvious cause.

## Security model

- **Authorization = chat membership** (Lark group, or Telegram DM/group). Anyone who
  can DM the bot or is in a group it was invited to can run any UseCase on any
  configured cluster. This is accepted because kato is strictly read-only against
  Kubernetes (`get/list/watch`).
- kato's API is unauthenticated; kato-bot is effectively the reach path to it. Network
  placement (in-cluster URLs, peering, NetworkPolicy) is the real boundary.
- `insecureSkipVerify` is per-cluster and opt-in, for self-signed certs on trusted
  networks; it leaves that hop MITM-exposed and is labelled as such everywhere.

## Testing strategy

No network, no live cluster, table tests throughout:

- `internal/kato` — against `httptest` servers returning kato's JSON shapes, including
  `400/422/429/5xx` → asserts the typed `*APIError` mapping.
- `internal/core` — `Handle` with a fake `Renderer` (records calls) and fake
  `KatoClient`: the picker→form→running→result sequence, required-input validation,
  cluster resolution, and error mapping. This is where the real logic coverage lives.
- `internal/platform/lark` — card builders asserted on their produced JSON; the decoder
  fed sample event payloads and asserted on the emitted `Intent`; dispatch tested for
  dedup and mention-gating.
- `internal/platform/telegram` — keyboard/message builders asserted on their rendered
  text and markup; the decoder and callback encode/decode fed sample payloads; the
  session store and dispatch tested for wizard transitions, TTL expiry, and dedup.

## Extending to another platform

Adding a platform means one new `internal/platform/<x>` package that (a) decodes
platform events into the `Intent` types, filling `Reply` with whatever that platform
needs to address/update a message, and (b) implements `Renderer` for that platform's
message format — with **zero changes** to `internal/core` or `internal/kato`, plus a
`New(cfg, deps) (platform.Adapter, error)` that `main.go` can wire in alongside the
others. Lark and Telegram are both now reference implementations of that contract —
Lark for a card-native platform, Telegram for one that needs message-edit and a
conversational fallback for input collection.

