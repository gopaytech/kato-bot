# kato-bot: Telegram platform adapter

{% raw %}

**Status:** Draft (design) — awaiting review

**Goal:** Add **Telegram** as a second chat platform alongside Lark, at **full feature
parity**: the interactive single-run flow (cluster → usecase → inputs → run → summary),
predefined **group/batch** runs with threaded progress, and the optional **LLM group
summary**. Telegram is a new `internal/platform/telegram` adapter that implements the
existing platform-agnostic ports with **no changes to `internal/core` or `internal/kato`**.
A new platform-neutral `internal/platform` package holds the single generic **`Adapter`
lifecycle seam** through which `main.go` runs Lark, Telegram, or both — one deployment can
serve either or both.

---

## Background

kato-bot is a thin ChatOps adapter over kato's REST API, built ports-and-adapters:
`internal/core` owns all orchestration and imports **no platform package**; a platform
package (Lark today) only (a) decodes platform events into `core.Intent` values and (b)
implements `core.Renderer` (and, for groups, `core.GroupReporter`) for that platform's
message format. The Lark package is the reference implementation of that contract; see
`ARCHITECTURE.md` and the v1 design `2026-06-16-kato-bot-design.md`.

The ports Telegram must satisfy (`internal/core/types.go`, `internal/core/group.go`):

- **`Intent`** (inbound): `ListClusters`, `PickCluster`, `PickUseCase`, `SubmitForm`,
  `PickGroup`, `RunGroup`.
- **`Renderer`** (outbound, 7 methods): `RenderClusterPicker`, `RenderPicker(ucs, groups)`,
  `RenderGroupConfirm`, `RenderForm(contract, prefill, formErr)`, `RenderRunning`,
  `RenderResult`, `RenderError`.
- **`Reply`** — opaque addressing threaded through everything (chat id, the bot message
  to update, selected cluster).
- **`core.GroupReporter`** (`Start` / `ServiceDone` / `Finish`) — driven by
  `GroupRunner.Run`, implemented per platform.
- **`platform.Adapter`** (lifecycle, **new** — in a platform-neutral `internal/platform`
  package, *not* `core`, since `main` drives it, not core): `Name()` + `Start(ctx)`, the
  single seam `main.go` uses to construct and run any platform uniformly (see *Generic
  adapter interface* below).

`core.Core.Handle` is fully synchronous and returns `(deferred, err)`; `deferred` is
non-nil **only** for a validated `SubmitForm` (the slow `kato.Run → RenderResult` work the
adapter runs in a goroutine). The core state machine is reused verbatim.

---

## Why Telegram is not a drop-in (design constraints)

Two Lark-specific capabilities the whole flow leans on **do not exist** on Telegram, and
they shape this design:

1. **No native form widget.** Lark collects a UseCase's inputs with Card JSON 2.0
   `form`/`input` components. Telegram inline keyboards are **buttons only** — free-text
   input values cannot be gathered from a keyboard.
2. **No "state in the button".** Lark stays fully stateless by encoding cluster/usecase/etc.
   in each button's `value` payload. Telegram's `callback_data` is capped at **64 bytes**,
   so a whole form's state cannot round-trip through a button.

Everything else maps cleanly: **long-polling** (`getUpdates`) fits the existing no-ingress,
single-replica, dial-out model; **message editing** (`editMessageText`) replaces Lark's
card patching; and Lark's two render paths (callback-response vs. patch) collapse to a
single path on Telegram (every update is a `sendMessage`/`editMessageText` call).

---

## Generic adapter interface (the platform seam)

kato-bot already talks *to* every platform through one generic contract — `core.Renderer`
+ `core.GroupReporter` outbound, and the `core.Intent` vocabulary inbound. What was missing
is a generic **lifecycle** seam so `cmd/kato-bot/main.go` can construct and run any platform
without knowing which. This design adds it as a small **platform-neutral package**,
`internal/platform` — deliberately *not* in `core`, because it is `main` that drives the
lifecycle, not `core` (so `core` stays free of even this):

```go
// internal/platform/platform.go
type Adapter interface {
    Name() string                    // "lark" | "telegram", for logs
    Start(ctx context.Context) error // blocks until ctx is cancelled
}

// Deps bundles the shared singletons every platform is wired from.
type Deps struct {
    Clusters                 *core.Registry
    Groups                   *core.GroupRegistry
    Runner                   *core.GroupRunner
    Summarizer               summary.Client // nil disables the LLM summary
    RunTimeout, GroupTimeout time.Duration
    LogLevel                 string
}

// NonNil drops the unconfigured (nil) platforms.
func NonNil(as ...Adapter) []Adapter { /* ... */ }
```

`internal/platform` imports `core` and `summary` (no cycle — neither imports it back);
`lark`, `telegram`, and `main` import it. Each platform exposes a constructor that
encapsulates *all* of its own wiring — its `Renderer`, its own `core.Core`, its event
loop — and returns a **nil** `Adapter` when that platform is not configured:

```go
func lark.New(cfg config.Config, d platform.Deps) (platform.Adapter, error)     // nil, nil if Lark creds absent
func telegram.New(cfg config.Config, d platform.Deps) (platform.Adapter, error) // nil, nil if token absent
```

**What this unifies — and what it deliberately doesn't.** The seam is thin: lifecycle only.
The adapter *bodies* stay bespoke (Lark SDK events vs. Telegram updates; callback-response
vs. message-edit; native form vs. wizard; per-platform group reporter), because forcing
those behind one interface would either push platform specifics into `core` (violating
"core imports no platform") or yield a lowest-common-denominator abstraction that serves
neither platform. Platforms meet at exactly **three seams** — `platform.Adapter`
(lifecycle), `core.Renderer`/`core.GroupReporter` (outbound), `core.Intent` (inbound) —
which is the right amount of generic.

This also **refactors the existing Lark wiring**: `lark.Adapter` already has
`Start(ctx) error`; it gains `Name()` and a `lark.New(...)` constructor, so today's
hand-wired Lark block moves out of `main.go` and Lark and Telegram are built and run
identically. Net simplification of `main.go`, not just an addition for Telegram.

---

## Decisions (from brainstorming)

- **Scope:** full parity (single-run **with inputs** + groups + LLM summary).
- **Input collection:** **conversational wizard** — the bot prompts each required input as
  a message; the user replies with text. Needs a small in-memory, TTL'd per-chat session
  (the bot's first server-side state; acceptable — see below).
- **Coexistence:** **both adapters, credential-gated.** Each starts if its credentials are
  present; run concurrently when both are; startup fails only if **neither** is set.
- **Transport:** long-polling (`getUpdates`). Webhook rejected (needs ingress).
- **Library:** `github.com/go-telegram/bot` (modern, maintained, context-first, zero-dep).
- **Formatting:** HTML parse mode (safer escaping than MarkdownV2); results chunked at
  Telegram's 4096-character message limit.
- **Group progress:** edit one progress message in place (analogue of Lark's morphing card).

---

## Architecture

New package `internal/platform/telegram/`, structured to mirror the Lark package:

```
internal/platform/telegram/
  dispatch.go       long-poll loop (getUpdates), update → Intent routing, group-run kickoff
  decode.go         update JSON → core.Intent (pure, library-free where practical)
  render.go         Renderer: send/edit Telegram messages + inline keyboards
  keyboards.go      pure inline-keyboard + message-text builders (asserted in tests)
  session.go        in-memory, TTL'd wizard session store (chatID,userID → pending flow)
  sender.go         Telegram Bot API calls (sendMessage / editMessageText / answerCallbackQuery)
  groupreporter.go  core.GroupReporter impl backed by a Telegram sender (edit-in-place)
  adapter.go        telegram.New(cfg, deps) → platform.Adapter (builds renderer + core.Core + loop)
```

`internal/core` and `internal/kato` are **untouched**. A new platform-neutral
`internal/platform` package (`Adapter`, `Deps`, `NonNil`) holds the lifecycle seam;
`cmd/kato-bot/main.go` and `internal/config` change to wire both adapters through it (below).

### Event flow

Mirrors Lark's two-event model. `getUpdates` returns three relevant update shapes:

| Telegram update | Decodes to | Notes |
|---|---|---|
| **message** (DM: any text; group: `@bot` or `/kato`) | `ListClusters` | gated like Lark's `shouldRespond` |
| **callback_query** (inline-keyboard tap) | `PickCluster` / `PickUseCase` / `PickGroup` / `RunGroup` | `answerCallbackQuery` sent promptly; work runs after |
| **message that replies to an active wizard prompt** | feeds the wizard (→ `SubmitForm`) | correlated via the session store, not a callback |

`update_id` gives natural ordering and dedup (offset acking), replacing Lark's redelivery
dedup set. Long-polling has no tight callback-ack window, so the fast-ack goroutine dance
is unnecessary; `callback_query` is still answered immediately so the client stops its
spinner, with the real work (a kato call + edit) done afterward.

### Renderer mapping

| Renderer method | Telegram action |
|---|---|
| `RenderClusterPicker` | `sendMessage` + inline keyboard (one button per cluster) |
| `RenderPicker(ucs, groups)` | `editMessageText` → usecase buttons + group buttons |
| `RenderGroupConfirm` | `editMessageText` → group detail + "Run" / "Run + summary" buttons |
| `RenderForm(contract, prefill, formErr)` | **wizard step** — prompt for next missing input (below) |
| `RenderRunning` | `editMessageText` → "⏳ running…" |
| `RenderResult` | `editMessageText` → summary (HTML; chunked at 4096) |
| `RenderError` | `editMessageText`/`sendMessage` → error text |

### The input wizard — driven by core's own loop

The central insight: interpreting `RenderForm` as **"prompt for the next missing input"**
makes core's existing `SubmitForm → missingRequired → RenderForm` cycle *be* the wizard,
with **no core changes**.

1. `PickUseCase` → core calls `RenderForm(contract, nil, "")`. The Telegram `RenderForm`
   creates/updates a **session** keyed by `(chatID, userID)` holding
   `{cluster, usecase, contract, collected{}, promptMsgID, deadline}` and sends
   "Send a value for `<firstMissingInput>`:".
2. The user's **text reply** is not a callback. The dispatcher looks up the session for
   `(chatID, userID)`, adds the value to `collected`, and synthesizes
   `SubmitForm{Reply: session.reply, Name: usecase, Inputs: collected}` into `core.Handle`.
3. Core validates:
   - still missing required → `RenderForm(prefill=collected, ...)` → adapter prompts the
     next input;
   - complete → `RenderRunning` + the deferred `Run`; the adapter runs it and edits the
     result into `promptMsgID`.
   A kato `400` on the run re-enters the form (core already does this) → the wizard
   re-prompts with the error — same behavior as Lark.

A UseCase with **zero inputs** yields no prompts: `RenderForm` sees nothing missing and
edits the message to a single **Run button** (`r|<cluster>|<usecase>`) — the explicit
submit Lark gets from its plain callback button. Tapping it decodes to
`SubmitForm{…, Inputs: {}}` and core proceeds straight to the run.

### Session store (`session.go`)

- **In-memory**, `sync.Mutex`-guarded. Keyed by **`(chatID, messageID)`** — the pair core
  hands back in every `Reply`, so the `Renderer` can find and update the pending flow from a
  `RenderForm`/`RenderResult` call (which carry no user id). A second index,
  **`(chatID, userID) → messageID`**, lets the dispatcher find the same session from a plain
  text reply (which carries a user id but no message id). The dispatcher populates both on
  `PickUseCase` (it has chat, user, and the tapped message id); the renderer addresses only
  by `(chatID, messageID)`. This bridges both directions **without adding a field to
  `core.Reply`**.
- Each entry holds `{userID, cluster, usecase, collected{}, next, deadline}`; the bot keeps
  editing the one wizard message, so `messageID` is stable across the flow.
- **TTL** (default ~10 min) and a **bounded size**; a background sweep drops expired entries.
  `RenderResult`/`RenderError` (and `/cancel`) end the session.
- This is the bot's **first server-side state**, acceptable because the deployment is
  **already single-replica** — Lark's random WebSocket event routing and the per-process run
  semaphore forbid scale-out regardless. A restart drops in-flight wizards only; the user
  restarts the flow. State is per-user and short.

### `callback_data` (64-byte cap)

kato cluster/usecase/group names are short k8s-style identifiers, so buttons encode the
action and the **names** directly, pipe-delimited (`|` never appears in those names) —
keeping the whole flow **stateless in the button**, exactly as Lark encodes state in a
button `value`:

| button | `callback_data` | decodes to |
|---|---|---|
| cluster | `c\|<cluster>` | `PickCluster{Cluster}` |
| usecase | `u\|<cluster>\|<usecase>` | `PickUseCase{Cluster, Name}` |
| run (zero-input) | `r\|<cluster>\|<usecase>` | `SubmitForm{Cluster, Name, {}}` |
| group | `g\|<cluster>\|<group>` | `PickGroup{Cluster, Name}` |
| run group | `rg\|<cluster>\|<group>\|<0\|1>` | `RunGroup{Cluster, Name, Summary}` |

The encoder guards the 64-byte limit (logs and skips a button whose payload would overflow —
unreachable for realistic kato names). Navigation stays stateless; the session store is used
**only** for the input wizard.

### Groups (full parity)

- `PickCluster`'s picker already lists `groups` alongside usecases (core passes both to
  `RenderPicker`); `PickGroup` → `RenderGroupConfirm`.
- `RunGroup` is handled **in the adapter** (as Lark does — it needs the platform reporter),
  not in `core.Handle`: acquire the shared `GroupRunner` lock (`TryAcquire`), run in a
  background goroutine bounded by `GroupTimeout`, and report through a new
  **`telegramGroupReporter` implementing `core.GroupReporter`**. Progress is shown by
  **editing one progress message in place**; on completion, if summary was requested (or the
  group defaults to it) an LLM summary message is sent via the shared `summary` client.
- The `GroupRunner`, `GroupRegistry`, and `summary.Client` are the **same instances** shared
  with the Lark adapter — a single source of truth for group definitions and summarization.

---

## Configuration, wiring, deployment

### Config (`internal/config`)

New fields / env:

| var | default | meaning |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | (none) | BotFather token; presence enables the Telegram adapter |
| `TELEGRAM_API_BASE_URL` | `https://api.telegram.org` | override for a local Bot API server |
| `TELEGRAM_POLL_TIMEOUT` | `30s` | `getUpdates` long-poll timeout |

**Invariant change.** Today `config.Load` hard-requires `LARK_APP_ID` + `LARK_APP_SECRET`.
New rule: **an adapter is enabled iff its credentials are present, and startup fails only
if neither platform is configured.** Concretely:

- Lark enabled ⇔ both `LARK_APP_ID` and `LARK_APP_SECRET` set.
- Telegram enabled ⇔ `TELEGRAM_BOT_TOKEN` set.
- If **neither** is enabled → `Load` returns an error (`at least one of Lark or Telegram
  must be configured`).
- A partially-configured Lark (one of the two creds) remains an error.

This updates the existing `internal/config/config_test.go` case that asserts an error when
Lark creds are absent.

### Wiring (`cmd/kato-bot/main.go`)

`main` builds the shared singletons once — `Registry`, `gateway`, `GroupRegistry`,
`GroupRunner`, optional `summary.Client` — bundles them into a `platform.Deps`, constructs
each platform through its `New`, and runs the enabled ones uniformly through the
`platform.Adapter` seam:

```go
deps := platform.Deps{
    Clusters: registry, Groups: groupReg, Runner: groupRunner, Summarizer: summarizer,
    RunTimeout: cfg.KatoRunTimeout, GroupTimeout: cfg.GroupRunTimeout, LogLevel: cfg.LogLevel,
}

lk, err := lark.New(cfg, deps)
if err != nil { log.Fatalf("lark init: %v", err) }
tg, err := telegram.New(cfg, deps)
if err != nil { log.Fatalf("telegram init: %v", err) }

adapters := platform.NonNil(lk, tg) // unconfigured platforms are nil and dropped
if len(adapters) == 0 {
    log.Fatal("configure Lark and/or Telegram") // belt-and-braces; config.Load already guards this
}
for _, a := range adapters {
    go func(a platform.Adapter) {
        if err := a.Start(ctx); err != nil && ctx.Err() == nil {
            log.Fatalf("%s adapter: %v", a.Name(), err)
        }
    }(a)
}
<-ctx.Done()
```

Each platform's `New` builds its **own** `core.Core` (sharing the same `Clusters`/`Groups`;
`Core` is tiny and stateless, so one per platform sidesteps any renderer multiplexing) plus
its renderer, session store, and event loop — so `main.go` holds **no** platform-specific
field wiring anymore. The `gateway`/`groupapi`, the health server, and the MCP + REST proxy
listener (`API_ADDR`) are independent of the chat adapters and remain in `main`, unchanged.

### Helm chart (`charts/kato-bot`)

- Telegram token Secret (chart-managed or `telegram.existingSecret`), values for
  `telegram.enabled`/`telegram.botToken`/`telegram.existingSecret`, and the new env on the
  Deployment.
- Lark values become optional; chart template guards so a Telegram-only (or Lark-only)
  install renders without the other platform's Secret. `README.md.gotmpl` env table +
  "Lark app setup" gains a "Telegram bot setup" sibling; regenerate both READMEs with
  `make readme`.
- Deployment stays **single-replica** (unchanged and still required).

---

## Testing (matches existing strategy — no network, table tests)

- `decode_test.go` — sample Telegram updates (message, callback_query, wizard reply) →
  asserted `Intent`.
- `keyboards_test.go` — inline-keyboard + message-text builders asserted on produced JSON
  (the Lark `cards_test.go` analogue).
- `session_test.go` — multi-turn wizard: prompt → reply → prompt → submit; TTL expiry and
  bounded eviction; zero-input fast path.
- `render_test.go` / `groupreporter_test.go` — `Renderer` and `core.GroupReporter` against
  a **fake sender** (records calls), including result chunking at 4096 chars.
- `dispatch_test.go` — update routing, group-run gating (`TryAcquire` full → "already
  running"), `update_id` dedup.
- `config_test.go` — the new both-gated validation matrix (Lark-only, Telegram-only, both,
  neither, partial-Lark).

`internal/core` and `internal/kato` tests are **unchanged** — the proof that the core
contract held.

---

## Files touched

- **New:** `internal/platform/telegram/{dispatch,decode,render,keyboards,session,sender,
  groupreporter,adapter}.go` + `_test.go` siblings.
- **New:** `internal/platform/platform.go` — the `Adapter` lifecycle interface, `Deps`
  bundle, and `NonNil` filter (platform-neutral; imports `core` + `summary`) + a test.
- `internal/platform/lark/` — gains `Name()` and a `lark.New(cfg, deps)` constructor, so the
  existing Lark wiring moves out of `main.go` and satisfies `platform.Adapter` (behavior
  unchanged; covered by existing tests + build).
- `internal/config/config.go` + `config_test.go` — Telegram env, both-gated validation.
- `cmd/kato-bot/main.go` — builds the shared `platform.Deps`, constructs platforms via
  `New(...)`, runs the enabled `[]platform.Adapter` uniformly; no platform-specific field
  wiring remains.
- `charts/kato-bot/` — Telegram Secret/values/env, optional-Lark guards.
- `charts/kato-bot/README.md.gotmpl` (+ regenerated `README.md` × 2) — Telegram env + setup.
- `ARCHITECTURE.md` — Telegram section; "v1 supports Lark; Telegram is the second adapter."
- `go.mod` / `go.sum` — `github.com/go-telegram/bot`.

---

## Non-goals

- **No webhook transport** (long-poll only; no ingress).
- **No persistent session store** (in-memory, TTL'd; single-replica assumption stands).
- **No cross-platform bridging** (a flow started on Telegram stays on Telegram).
- **No Telegram-native niceties** beyond parity (no inline-query mode, no slash-command
  menu registration beyond `/kato`, no per-user auth — access is Telegram chat membership,
  mirroring the "authorization = Lark membership" stance; kato stays read-only).
- **No scale-out / multi-replica** (unchanged constraint).

{% endraw %}
