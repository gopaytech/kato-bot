# Telegram Platform Adapter Implementation Plan
{% raw %}
> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Telegram as a second chat platform alongside Lark at full feature parity (single-run with inputs, predefined group runs, and the optional LLM group summary), wired through one generic adapter seam.

**Architecture:** A new `internal/platform/telegram` package implements the existing platform-agnostic ports (`core.Renderer`, `core.GroupReporter`, decoding events into `core.Intent`) with no changes to `internal/core` or `internal/kato`. A new platform-neutral `internal/platform` package holds a small `Adapter` lifecycle interface + `Deps` bundle + `NonNil` filter; `main.go` constructs each platform via `New(cfg, deps)` and runs the enabled ones concurrently. Telegram uses long-polling; the input form becomes a conversational wizard backed by an in-memory, TTL'd session store; message-editing replaces Lark's card-patching.

**Tech Stack:** Go 1.25, `github.com/go-telegram/bot` (long-poll Bot API client), standard `testing` with table tests, Helm.

**Spec:** `docs/superpowers/specs/2026-09-06-telegram-adapter-design.md`

## Global Constraints

- Module path: `github.com/gopaytech/kato-bot`. Go floor: `go 1.25.0` (go.mod).
- No changes to `internal/core` or `internal/kato`. Their existing tests MUST stay green.
- Tests: `make test` (= `go test -race ./...`). No network, no live cluster — table tests with fakes only.
- `gofmt` clean and `go vet ./...` clean before every commit.
- New dependency: `github.com/go-telegram/bot` — pin the exact latest version at implement time; add via `go get`. Any code calling this library carries a `// VERIFY` comment naming the version it was confirmed against (repo convention — see `internal/platform/lark/sender.go`).
- Telegram message formatting: HTML parse mode (`models.ParseModeHTML`). Result text chunked at Telegram's 4096-character limit.
- Deployment stays single-replica (unchanged; still required).
- Commit after each task using Conventional Commits (`feat:`/`refactor:`/`test:`/`chore:`/`docs:`). We are on `main`; create a branch `telegram-adapter` before Task 1.

---

## File Structure

**New — generic seam:**
- `internal/platform/platform.go` — `Adapter` interface, `Deps` struct, `NonNil` helper. Platform-neutral; imports `core` + `summary`.
- `internal/platform/platform_test.go`

**New — Telegram adapter (`internal/platform/telegram/`):**
- `sender.go` — `sender`/`groupSender` interfaces + real `apiSender` over `github.com/go-telegram/bot` (library-touching; build-only).
- `callback.go` — `callback_data` encode/decode (pure).
- `keyboards.go` — message-text + inline-keyboard builders + 4096 chunking (pure; imports `models`).
- `session.go` — in-memory TTL'd wizard session store (pure).
- `groupreporter.go` — `core.GroupReporter` (edit-in-place) over `groupSender`.
- `render.go` — `core.Renderer` (7 methods) over `sender` + session store; the wizard logic.
- `decode.go` — Telegram update → `core.Intent` (pure over small structs).
- `dispatch.go` — routing (message / callback / wizard-reply), `update_id` dedup, run semaphore, group-run kickoff.
- `adapter.go` — `telegram.New(cfg, deps) (platform.Adapter, error)`; `Adapter` with `Name()` + `Start(ctx)` running the long-poll loop.
- `*_test.go` siblings for `callback`, `keyboards`, `session`, `groupreporter`, `render`, `decode`, `dispatch`.

**Modified:**
- `internal/platform/lark/adapter.go` (new small file) — `Name()` + `lark.New(cfg, deps)` constructor (moves wiring out of `main.go`).
- `internal/config/config.go` + `config_test.go` — Telegram env + both-gated validation.
- `cmd/kato-bot/main.go` — build `Deps`, construct platforms via `New`, run `[]platform.Adapter`.
- `charts/kato-bot/` — Telegram Secret, values, deployment env, optional-Lark guards.
- `charts/kato-bot/README.md.gotmpl` (+ regenerated READMEs), `ARCHITECTURE.md`.
- `go.mod` / `go.sum`.

---

## Task 1: Generic adapter seam (`internal/platform`)

**Files:**
- Create: `internal/platform/platform.go`
- Test: `internal/platform/platform_test.go`

**Interfaces:**
- Consumes: `core.Registry`, `core.GroupRegistry`, `core.GroupRunner` (existing); `summary.Client` (existing, `internal/summary/openai.go`).
- Produces: `platform.Adapter` interface (`Name() string`, `Start(context.Context) error`); `platform.Deps` struct; `func NonNil(as ...Adapter) []Adapter`.

- [ ] **Step 1: Create the branch**

```bash
git checkout -b telegram-adapter
```

- [ ] **Step 2: Write the failing test**

Create `internal/platform/platform_test.go`:

```go
package platform

import (
	"context"
	"testing"
)

type fakeAdapter struct{ name string }

func (f fakeAdapter) Name() string                          { return f.name }
func (f fakeAdapter) Start(_ context.Context) error         { return nil }

func TestNonNilDropsNilAdapters(t *testing.T) {
	a := fakeAdapter{name: "lark"}
	b := fakeAdapter{name: "telegram"}

	got := NonNil(a, nil, b, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 adapters, got %d", len(got))
	}
	if got[0].Name() != "lark" || got[1].Name() != "telegram" {
		t.Fatalf("order/identity wrong: %q, %q", got[0].Name(), got[1].Name())
	}
}

func TestNonNilAllNil(t *testing.T) {
	if got := NonNil(nil, nil); len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/platform/ -run TestNonNil -v`
Expected: FAIL — package/`NonNil` undefined (build error).

- [ ] **Step 4: Write the implementation**

Create `internal/platform/platform.go`:

```go
// Package platform holds the generic lifecycle seam through which cmd/kato-bot
// runs any chat platform (Lark, Telegram) uniformly. It is platform-neutral: it
// imports core and summary, and neither imports it back.
package platform

import (
	"context"
	"time"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/summary"
)

// Adapter is one running chat platform. main.go constructs the enabled ones and
// runs each Start in its own goroutine. Start blocks until ctx is cancelled.
type Adapter interface {
	Name() string
	Start(ctx context.Context) error
}

// Deps bundles the shared singletons every platform is wired from. One set is
// built in main and passed to each platform's New.
type Deps struct {
	Clusters     *core.Registry
	Groups       *core.GroupRegistry
	Runner       *core.GroupRunner
	Summarizer   summary.Client // nil disables the LLM group summary
	RunTimeout   time.Duration
	GroupTimeout time.Duration
	MaxConcurrent int
	LogLevel     string
	SummaryMaxEvidenceBytes int
}

// NonNil returns the non-nil adapters in order. A platform's New returns a nil
// Adapter when it is not configured; NonNil drops those.
func NonNil(as ...Adapter) []Adapter {
	out := make([]Adapter, 0, len(as))
	for _, a := range as {
		if a != nil {
			out = append(out, a)
		}
	}
	return out
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/platform/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/platform.go internal/platform/platform_test.go
git commit -m "feat: add generic platform.Adapter seam (Adapter, Deps, NonNil)"
```

---

## Task 2: Lark satisfies the seam (`lark.New` + `Name`)

Refactor the existing Lark wiring out of `main.go` into a `lark.New(cfg, deps)` constructor, and make `lark.Adapter` satisfy `platform.Adapter`. Behavior is unchanged; this is a pure refactor proven by the existing Lark tests + build.

**Files:**
- Create: `internal/platform/lark/adapter.go`
- Reference (do not duplicate wiring): `cmd/kato-bot/main.go:29-91` (current Lark construction), `internal/platform/lark/render.go:19` (`NewSender`), `internal/platform/lark/dispatch.go:24-46,122` (`Adapter` struct + `Start`).

**Interfaces:**
- Consumes: `config.Config`, `platform.Deps`, `lark.NewSender(appID, appSecret, baseURL) *Renderer`, existing `lark.Adapter`.
- Produces: `func lark.New(cfg config.Config, d platform.Deps) (platform.Adapter, error)` (returns `nil, nil` when Lark creds absent); `func (*Adapter) Name() string`.

- [ ] **Step 1: Add `Name()` to the existing Adapter**

In `internal/platform/lark/dispatch.go`, add after the `Adapter` struct:

```go
// Name identifies this platform for logs and the generic runner.
func (a *Adapter) Name() string { return "lark" }
```

- [ ] **Step 2: Write the constructor**

Create `internal/platform/lark/adapter.go`:

```go
package lark

import (
	"strings"

	"github.com/gopaytech/kato-bot/internal/config"
	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

// New builds the Lark adapter from config + shared deps, or returns (nil, nil)
// when Lark is not configured (no app id/secret). It encapsulates the renderer +
// its own core.Core so main.go holds no Lark-specific wiring.
func New(cfg config.Config, d platform.Deps) (platform.Adapter, error) {
	if strings.TrimSpace(cfg.LarkAppID) == "" || strings.TrimSpace(cfg.LarkAppSecret) == "" {
		return nil, nil
	}
	renderer := NewSender(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkBaseURL)
	c := &core.Core{Clusters: d.Clusters, Groups: d.Groups, R: renderer}
	return &Adapter{
		AppID:         cfg.LarkAppID,
		AppSecret:     cfg.LarkAppSecret,
		Core:          c,
		R:             renderer,
		RunTimeout:    d.RunTimeout,
		LogLevel:      d.LogLevel,
		MaxConcurrent: d.MaxConcurrent,
		BaseURL:       cfg.LarkBaseURL,

		Groups:       d.Groups,
		GroupRunner:  d.Runner,
		GroupTimeout: d.GroupTimeout,

		Summarizer:              d.Summarizer,
		SummaryMaxEvidenceBytes: d.SummaryMaxEvidenceBytes,
	}, nil
}
```

- [ ] **Step 3: Verify it builds and Lark tests pass**

Run: `go build ./... && go test ./internal/platform/lark/ -v`
Expected: build OK; all existing Lark tests PASS (behavior unchanged).

- [ ] **Step 4: Confirm the interface is satisfied**

Add a compile-time assertion at the bottom of `internal/platform/lark/adapter.go`:

```go
var _ platform.Adapter = (*Adapter)(nil)
```

Run: `go build ./...`
Expected: OK (fails to compile if `Adapter` doesn't satisfy the seam).

- [ ] **Step 5: Commit**

```bash
git add internal/platform/lark/adapter.go internal/platform/lark/dispatch.go
git commit -m "refactor: add lark.New constructor + Name to satisfy platform.Adapter"
```

---

## Task 3: main.go runs platforms through the seam (Lark only)

Rewrite `main.go`'s adapter section to build `platform.Deps` and run `platform.NonNil(lark.New(...))` in a goroutine, blocking on ctx. Telegram is added in Task 13. This is the milestone that proves the generic loop with behavior unchanged.

**Files:**
- Modify: `cmd/kato-bot/main.go` (replace the Lark construction block `:29`, `:61`, `:75-91`, and the final `adapter.Start` block `:124-132`).

**Interfaces:**
- Consumes: `platform.Deps`, `lark.New`, `platform.NonNil`.
- Produces: nothing new (wiring only).

- [ ] **Step 1: Replace the renderer/adapter construction**

In `cmd/kato-bot/main.go`, delete the standalone `renderer := lark.NewSender(...)` line (`:29`), the `c := &core.Core{...}` line (`:61`), and the whole `adapter := &lark.Adapter{...}` literal (`:75-91`). Keep the registry/gateway/group-registry/groupRunner/summarizer construction (`:31-73`) and the health + api server blocks (`:97-122`). After the summarizer block, insert:

```go
	deps := platform.Deps{
		Clusters:                registry,
		Groups:                  groupReg,
		Runner:                  groupRunner,
		Summarizer:              summarizer,
		RunTimeout:              cfg.KatoRunTimeout,
		GroupTimeout:            cfg.GroupRunTimeout,
		MaxConcurrent:           cfg.MaxConcurrentRuns,
		LogLevel:                cfg.LogLevel,
		SummaryMaxEvidenceBytes: cfg.GroupSummary.MaxEvidenceBytes,
	}

	lk, err := lark.New(cfg, deps)
	if err != nil {
		log.Fatalf("lark init: %v", err)
	}
	adapters := platform.NonNil(lk)
	if len(adapters) == 0 {
		log.Fatal("configure Lark and/or Telegram")
	}
```

- [ ] **Step 2: Replace the blocking start**

Replace the final `if err := adapter.Start(ctx); ...` block (`:129-131`) with:

```go
	for _, a := range adapters {
		go func(a platform.Adapter) {
			if err := a.Start(ctx); err != nil && ctx.Err() == nil {
				log.Fatalf("%s adapter: %v", a.Name(), err)
			}
		}(a)
	}
	<-ctx.Done()
```

- [ ] **Step 3: Fix imports**

Add `"github.com/gopaytech/kato-bot/internal/platform"` to the import block. Keep the `lark` import. Remove the now-unused `core` import only if nothing else in `main.go` uses it (the gateway/groupapi construction may still use `core.Cluster`/`core.Group` — keep `core` if so). Run `gofmt -w cmd/kato-bot/main.go`.

- [ ] **Step 4: Build, vet, and full test**

Run: `go build ./... && go vet ./... && make test`
Expected: build/vet clean; all tests PASS.

- [ ] **Step 5: Smoke-run wiring (no creds → clean fatal)**

Run: `LARK_APP_ID= LARK_APP_SECRET= KATO_CLUSTERS_FILE=/dev/null go run ./cmd/kato-bot 2>&1 | head -3`
Expected: fails fast in `config.Load` (clusters file invalid or Lark required) — confirms startup still guards. (Full run needs real creds; not required here.)

- [ ] **Step 6: Commit**

```bash
git add cmd/kato-bot/main.go
git commit -m "refactor: run chat platforms through the generic platform.Adapter loop"
```

---

## Task 4: Config — Telegram env + both-gated validation

Add Telegram fields to `config.Config`, load them, and change the invariant from "Lark required" to "at least one platform configured".

**Files:**
- Modify: `internal/config/config.go` (`Config` struct `:55-70`, `Load` `:76-91`).
- Test: `internal/config/config_test.go`.

**Interfaces:**
- Consumes: env vars `TELEGRAM_BOT_TOKEN`, `TELEGRAM_API_BASE_URL`, `TELEGRAM_POLL_TIMEOUT`.
- Produces: `Config.TelegramBotToken string`, `Config.TelegramAPIBaseURL string`, `Config.TelegramPollTimeout time.Duration`. New validation rule.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go` (adapt the existing helpers there for setting env + a temp clusters file; the file already has a valid-clusters helper — reuse it). These tests assume a helper `writeClusters(t)` returning a path to a one-cluster file; if the existing test uses a different name, use that:

```go
func TestLoadTelegramOnlyOK(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("telegram-only should load: %v", err)
	}
	if cfg.TelegramBotToken != "123:abc" {
		t.Fatalf("token not loaded: %q", cfg.TelegramBotToken)
	}
	if cfg.TelegramAPIBaseURL != "https://api.telegram.org" {
		t.Fatalf("default api base wrong: %q", cfg.TelegramAPIBaseURL)
	}
	if cfg.TelegramPollTimeout != 30*time.Second {
		t.Fatalf("default poll timeout wrong: %v", cfg.TelegramPollTimeout)
	}
}

func TestLoadNeitherPlatformFails(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t))

	if _, err := Load(); err == nil {
		t.Fatal("expected error when neither platform is configured")
	}
}

func TestLoadPartialLarkFails(t *testing.T) {
	t.Setenv("LARK_APP_ID", "cli_x")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t))

	if _, err := Load(); err == nil {
		t.Fatal("expected error when Lark is half-configured and Telegram absent")
	}
}
```

If `writeClusters` does not already exist in the test file, add:

```go
func writeClusters(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clusters.yaml")
	if err := os.WriteFile(p, []byte("clusters:\n  - name: default\n    url: http://kato:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
```

(Ensure imports `os`, `path/filepath`, `time` are present.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestLoadTelegram|TestLoadNeither|TestLoadPartial' -v`
Expected: FAIL — `TelegramBotToken` field undefined / old "LARK required" logic rejects Telegram-only.

- [ ] **Step 3: Add config fields**

In `internal/config/config.go`, add to the `Config` struct:

```go
	// Telegram bot: presence of TelegramBotToken enables the Telegram adapter.
	TelegramBotToken    string
	TelegramAPIBaseURL  string
	TelegramPollTimeout time.Duration
```

- [ ] **Step 4: Replace the Lark-required guard with both-gated validation**

In `Load`, delete the block (`:89-91`):

```go
	if strings.TrimSpace(cfg.LarkAppID) == "" || strings.TrimSpace(cfg.LarkAppSecret) == "" {
		return Config{}, fmt.Errorf("LARK_APP_ID and LARK_APP_SECRET are required")
	}
```

and, after the `LarkBaseURL` default is set in the struct literal, add Telegram loading + the new validation:

```go
	cfg.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramAPIBaseURL = envOr("TELEGRAM_API_BASE_URL", "https://api.telegram.org")
	cfg.TelegramPollTimeout = 30 * time.Second
	if v := os.Getenv("TELEGRAM_POLL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("TELEGRAM_POLL_TIMEOUT: %w", err)
		}
		cfg.TelegramPollTimeout = d
	}

	larkID, larkSecret := strings.TrimSpace(cfg.LarkAppID), strings.TrimSpace(cfg.LarkAppSecret)
	larkEnabled := larkID != "" && larkSecret != ""
	larkPartial := (larkID != "") != (larkSecret != "")
	telegramEnabled := strings.TrimSpace(cfg.TelegramBotToken) != ""
	if larkPartial {
		return Config{}, fmt.Errorf("LARK_APP_ID and LARK_APP_SECRET must be set together")
	}
	if !larkEnabled && !telegramEnabled {
		return Config{}, fmt.Errorf("configure at least one platform: set LARK_APP_ID+LARK_APP_SECRET and/or TELEGRAM_BOT_TOKEN")
	}
```

(Place this after the clusters/groups are loaded so a Telegram-only run still validates clusters. Confirm `strings` and `time` are imported — they are.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — including any pre-existing config tests. If an existing test asserted the old "LARK required" error message for the both-absent case, update it to the new message.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: config gates Lark and Telegram independently; require at least one"
```

---

## Task 5: Telegram sender interfaces + real Bot API wrapper

Introduce the Telegram library and the thin `sender`/`groupSender` interfaces the renderer + reporter depend on. The real wrapper is library-touching and build-verified (its methods are exercised end-to-end at runtime, not unit-tested — mirroring `lark/sender.go`).

**Files:**
- Create: `internal/platform/telegram/sender.go`
- Modify: `go.mod`, `go.sum`.

**Interfaces:**
- Consumes: `github.com/go-telegram/bot`, `github.com/go-telegram/bot/models`.
- Produces:
  - `type sender interface { Send(ctx, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error); Edit(ctx, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error; Answer(ctx, callbackQueryID string) error }`
  - `type groupSender interface { Send(...) (int, error); Edit(...) error }` (subset of `sender`)
  - `type apiSender struct { b *bot.Bot }` implementing both.
  - `func newAPISender(b *bot.Bot) *apiSender`.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/go-telegram/bot@latest`
Then record the resolved version (from `go.mod`) — use it in the `// VERIFY` comment below.

- [ ] **Step 2: Write the interfaces + wrapper**

Create `internal/platform/telegram/sender.go`:

```go
package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// sender is the minimal Telegram message surface the renderer needs. The real
// impl (apiSender) wraps github.com/go-telegram/bot; tests use a fake.
type sender interface {
	// Send posts a new HTML message with an optional inline keyboard; returns the
	// new message id.
	Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error)
	// Edit replaces the text + keyboard of an existing bot message.
	Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error
	// Answer acknowledges a callback query so the client stops its spinner.
	Answer(ctx context.Context, callbackQueryID string) error
}

// groupSender is the subset the group reporter needs (no callback answering).
type groupSender interface {
	Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error)
	Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error
}

// apiSender implements sender using the go-telegram/bot client.
type apiSender struct{ b *bot.Bot }

func newAPISender(b *bot.Bot) *apiSender { return &apiSender{b: b} }

// VERIFY (go-telegram/bot API): confirmed against <PIN THE VERSION FROM STEP 1>.
//   - b.SendMessage(ctx, &bot.SendMessageParams{ChatID, Text, ParseMode, ReplyMarkup}) (*models.Message, error)
//   - b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID, MessageID, Text, ParseMode, ReplyMarkup}) (*models.Message, error)
//   - b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID}) (bool, error)
//   - ChatID fields accept an int64; ReplyMarkup accepts models.InlineKeyboardMarkup (value, not pointer).
func (s *apiSender) Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error) {
	p := &bot.SendMessageParams{ChatID: chatID, Text: html, ParseMode: models.ParseModeHTML}
	if kb != nil {
		p.ReplyMarkup = *kb
	}
	m, err := s.b.SendMessage(ctx, p)
	if err != nil {
		return 0, err
	}
	return m.ID, nil
}

func (s *apiSender) Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error {
	p := &bot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: html, ParseMode: models.ParseModeHTML}
	if kb != nil {
		p.ReplyMarkup = *kb
	}
	_, err := s.b.EditMessageText(ctx, p)
	return err
}

func (s *apiSender) Answer(ctx context.Context, callbackQueryID string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackQueryID})
	return err
}
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./internal/platform/telegram/`
Expected: OK. If the library's param field names differ from the VERIFY block, fix them and update the VERIFY comment to match the real signatures.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum internal/platform/telegram/sender.go
git commit -m "feat: add Telegram Bot API sender wrapper + go-telegram/bot dep"
```

---

## Task 6: `callback_data` codec

Pure encode/decode of button payloads. Names-based, pipe-delimited, 64-byte-guarded (see spec's callback_data table).

**Files:**
- Create: `internal/platform/telegram/callback.go`
- Test: `internal/platform/telegram/callback_test.go`

**Interfaces:**
- Produces:
  - `func cbCluster(cluster string) string` → `"c|<cluster>"`
  - `func cbUseCase(cluster, uc string) string` → `"u|<cluster>|<uc>"`
  - `func cbRun(cluster, uc string) string` → `"r|<cluster>|<uc>"`
  - `func cbGroup(cluster, g string) string` → `"g|<cluster>|<g>"`
  - `func cbRunGroup(cluster, g string, summary bool) string` → `"rg|<cluster>|<g>|<0|1>"`
  - `func decodeCB(data string) (core.Intent, error)` — builds the intent with an empty `Reply` (dispatch fills `Reply` addressing).
  - `const cbMaxBytes = 64`; `func cbFits(data string) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/platform/telegram/callback_test.go`:

```go
package telegram

import (
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestCallbackRoundTrip(t *testing.T) {
	cases := []struct {
		data string
		want core.Intent
	}{
		{cbCluster("prod"), core.PickCluster{Reply: core.Reply{Cluster: "prod"}}},
		{cbUseCase("prod", "deploy-check"), core.PickUseCase{Reply: core.Reply{Cluster: "prod"}, Name: "deploy-check"}},
		{cbRun("prod", "deploy-check"), core.SubmitForm{Reply: core.Reply{Cluster: "prod"}, Name: "deploy-check", Inputs: map[string]string{}}},
		{cbGroup("prod", "critical"), core.PickGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical"}},
		{cbRunGroup("prod", "critical", true), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical", Summary: true}},
		{cbRunGroup("prod", "critical", false), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical", Summary: false}},
	}
	for _, c := range cases {
		got, err := decodeCB(c.data)
		if err != nil {
			t.Fatalf("decode %q: %v", c.data, err)
		}
		if !intentEqual(got, c.want) {
			t.Fatalf("decode %q = %#v, want %#v", c.data, got, c.want)
		}
	}
}

func TestCallbackUnknownAction(t *testing.T) {
	if _, err := decodeCB("z|prod|x"); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestCallbackFits(t *testing.T) {
	if !cbFits(cbUseCase("prod", "deployment-troubleshooting")) {
		t.Fatal("realistic payload should fit 64 bytes")
	}
}

// intentEqual compares the intent variants this package produces (ignores Reply
// addressing beyond Cluster, which is all decodeCB sets).
func intentEqual(a, b core.Intent) bool {
	switch av := a.(type) {
	case core.PickCluster:
		bv, ok := b.(core.PickCluster)
		return ok && av.Reply.Cluster == bv.Reply.Cluster
	case core.PickUseCase:
		bv, ok := b.(core.PickUseCase)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name
	case core.SubmitForm:
		bv, ok := b.(core.SubmitForm)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name && len(av.Inputs) == len(bv.Inputs)
	case core.PickGroup:
		bv, ok := b.(core.PickGroup)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name
	case core.RunGroup:
		bv, ok := b.(core.RunGroup)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name && av.Summary == bv.Summary
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/telegram/ -run TestCallback -v`
Expected: FAIL — codec functions undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/callback.go`:

```go
package telegram

import (
	"fmt"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

// cbMaxBytes is Telegram's callback_data limit.
const cbMaxBytes = 64

func cbFits(data string) bool { return len(data) <= cbMaxBytes }

func cbCluster(cluster string) string          { return "c|" + cluster }
func cbUseCase(cluster, uc string) string       { return "u|" + cluster + "|" + uc }
func cbRun(cluster, uc string) string           { return "r|" + cluster + "|" + uc }
func cbGroup(cluster, g string) string          { return "g|" + cluster + "|" + g }
func cbRunGroup(cluster, g string, sum bool) string {
	flag := "0"
	if sum {
		flag = "1"
	}
	return "rg|" + cluster + "|" + g + "|" + flag
}

// decodeCB parses a callback_data string into an intent with Cluster/Name filled.
// The addressing part of Reply (ChatID, MessageID) is set by the dispatcher.
func decodeCB(data string) (core.Intent, error) {
	parts := strings.Split(data, "|")
	switch parts[0] {
	case "c":
		if len(parts) != 2 {
			return nil, fmt.Errorf("bad c payload %q", data)
		}
		return core.PickCluster{Reply: core.Reply{Cluster: parts[1]}}, nil
	case "u":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad u payload %q", data)
		}
		return core.PickUseCase{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2]}, nil
	case "r":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad r payload %q", data)
		}
		return core.SubmitForm{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2], Inputs: map[string]string{}}, nil
	case "g":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad g payload %q", data)
		}
		return core.PickGroup{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2]}, nil
	case "rg":
		if len(parts) != 4 {
			return nil, fmt.Errorf("bad rg payload %q", data)
		}
		return core.RunGroup{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2], Summary: parts[3] == "1"}, nil
	default:
		return nil, fmt.Errorf("unknown callback action %q", parts[0])
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/telegram/ -run TestCallback -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/telegram/callback.go internal/platform/telegram/callback_test.go
git commit -m "feat: add Telegram callback_data codec"
```

---

## Task 7: Keyboards, message text, and 4096 chunking

Pure builders that turn semantic state into `(html string, *models.InlineKeyboardMarkup)`. One builder per renderer state, plus a helper that chunks long result text at Telegram's 4096 limit and an HTML-escape helper.

**Files:**
- Create: `internal/platform/telegram/keyboards.go`
- Test: `internal/platform/telegram/keyboards_test.go`

**Interfaces:**
- Consumes: `core.Cluster`, `core.UseCase`, `core.Group`, `core.Contract`, `core.RunResult`; `cbCluster`/`cbUseCase`/`cbRun`/`cbGroup`/`cbRunGroup` (Task 6); `models.InlineKeyboardMarkup`/`models.InlineKeyboardButton`.
- Produces:
  - `func clusterPickerKB(clusters []core.Cluster) (string, *models.InlineKeyboardMarkup)`
  - `func pickerKB(cluster string, ucs []core.UseCase, groups []core.Group) (string, *models.InlineKeyboardMarkup)`
  - `func groupConfirmKB(g core.Group) (string, *models.InlineKeyboardMarkup)`
  - `func promptText(cluster, uc, next, formErr string) string` — wizard prompt (no keyboard; a Cancel note)
  - `func runButtonKB(cluster, uc string) (string, *models.InlineKeyboardMarkup)` — zero-input Run button
  - `func runningText(cluster, uc string, inputs map[string]string) string`
  - `func resultText(cluster, uc string, inputs map[string]string, res core.RunResult) string`
  - `func errorText(msg string) string`
  - `func chunk4096(s string) []string`
  - `func esc(s string) string` — HTML-escape for text nodes.

- [ ] **Step 1: Write the failing tests**

Create `internal/platform/telegram/keyboards_test.go`:

```go
package telegram

import (
	"strings"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestClusterPickerKB(t *testing.T) {
	_, kb := clusterPickerKB([]core.Cluster{{Name: "prod", Label: "Production"}, {Name: "dev"}})
	if kb == nil || len(kb.InlineKeyboard) != 2 {
		t.Fatalf("want 2 rows, got %#v", kb)
	}
	b0 := kb.InlineKeyboard[0][0]
	if b0.Text != "Production" || b0.CallbackData != "c|prod" {
		t.Fatalf("row0 wrong: text=%q data=%q", b0.Text, b0.CallbackData)
	}
	// Label defaults to Name when empty.
	if kb.InlineKeyboard[1][0].Text != "dev" {
		t.Fatalf("row1 label default wrong: %q", kb.InlineKeyboard[1][0].Text)
	}
}

func TestPickerKBSplitsUsecasesAndGroups(t *testing.T) {
	_, kb := pickerKB("prod",
		[]core.UseCase{{Name: "deploy-check"}},
		[]core.Group{{Name: "critical"}},
	)
	var datas []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			datas = append(datas, b.CallbackData)
		}
	}
	joined := strings.Join(datas, ",")
	if !strings.Contains(joined, "u|prod|deploy-check") || !strings.Contains(joined, "g|prod|critical") {
		t.Fatalf("missing usecase/group buttons: %s", joined)
	}
}

func TestGroupConfirmKBHasBothRunButtons(t *testing.T) {
	_, kb := groupConfirmKB(core.Group{Name: "critical", Cluster: "prod"})
	var datas []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			datas = append(datas, b.CallbackData)
		}
	}
	j := strings.Join(datas, ",")
	if !strings.Contains(j, "rg|prod|critical|0") || !strings.Contains(j, "rg|prod|critical|1") {
		t.Fatalf("want run and run+summary buttons: %s", j)
	}
}

func TestPromptTextEscapesAndNamesInput(t *testing.T) {
	got := promptText("prod", "deploy-check", "namespace", "required: namespace")
	if !strings.Contains(got, "namespace") || !strings.Contains(got, "required: namespace") {
		t.Fatalf("prompt missing input/err: %q", got)
	}
}

func TestResultTextEscapesHTML(t *testing.T) {
	res := core.RunResult{Summary: "1 < 2 & ok", Phase: "Succeeded"}
	got := resultText("prod", "uc", map[string]string{}, res)
	if strings.Contains(got, "1 < 2 &") || !strings.Contains(got, "1 &lt; 2 &amp; ok") {
		t.Fatalf("summary not HTML-escaped: %q", got)
	}
}

func TestChunk4096(t *testing.T) {
	long := strings.Repeat("x", 4096*2+10)
	parts := chunk4096(long)
	if len(parts) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 4096 {
			t.Fatalf("chunk %d too long: %d", i, len(p))
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/telegram/ -run 'KB|PromptText|ResultText|Chunk' -v`
Expected: FAIL — builders undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/keyboards.go`:

```go
package telegram

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/gopaytech/kato-bot/internal/core"
)

const tgMaxMessage = 4096

// esc HTML-escapes a text node for Telegram's HTML parse mode.
func esc(s string) string { return html.EscapeString(s) }

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func clusterPickerKB(clusters []core.Cluster) (string, *models.InlineKeyboardMarkup) {
	rows := make([][]models.InlineKeyboardButton, 0, len(clusters))
	for _, c := range clusters {
		label := c.Label
		if label == "" {
			label = c.Name
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, cbCluster(c.Name))})
	}
	return "<b>Pick a cluster</b>", &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func pickerKB(cluster string, ucs []core.UseCase, groups []core.Group) (string, *models.InlineKeyboardMarkup) {
	rows := make([][]models.InlineKeyboardButton, 0, len(ucs)+len(groups))
	for _, uc := range ucs {
		label := uc.Name
		if !uc.Ready {
			label = "⚠️ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, cbUseCase(cluster, uc.Name))})
	}
	for _, g := range groups {
		rows = append(rows, []models.InlineKeyboardButton{btn("👥 "+g.Name, cbGroup(cluster, g.Name))})
	}
	text := fmt.Sprintf("<b>Cluster:</b> %s\nPick a use case%s", esc(cluster), groupsHint(groups))
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func groupsHint(groups []core.Group) string {
	if len(groups) == 0 {
		return ":"
	}
	return " or a group:"
}

func groupConfirmKB(g core.Group) (string, *models.InlineKeyboardMarkup) {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Group:</b> %s\n<b>Cluster:</b> %s\n", esc(g.Name), esc(g.Cluster))
	for _, uc := range g.UseCaseCounts() {
		fmt.Fprintf(&b, "• %s — %d target(s)\n", esc(uc.UseCase), uc.Targets)
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{btn("▶️ Run", cbRunGroup(g.Cluster, g.Name, false))},
		{btn("▶️ Run + summary", cbRunGroup(g.Cluster, g.Name, true))},
	}}
	return b.String(), kb
}

func promptText(cluster, uc, next, formErr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b> on <b>%s</b>\n", esc(uc), esc(cluster))
	if formErr != "" {
		fmt.Fprintf(&b, "⚠️ %s\n", esc(formErr))
	}
	fmt.Fprintf(&b, "Send a value for <code>%s</code> (or /cancel).", esc(next))
	return b.String()
}

func runButtonKB(cluster, uc string) (string, *models.InlineKeyboardMarkup) {
	text := fmt.Sprintf("<b>%s</b> on <b>%s</b>\nNo inputs required.", esc(uc), esc(cluster))
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{btn("▶️ Run", cbRun(cluster, uc))},
	}}
	return text, kb
}

func runningText(cluster, uc string, inputs map[string]string) string {
	return fmt.Sprintf("⏳ Running <b>%s</b> on <b>%s</b>%s", esc(uc), esc(cluster), inputsLine(inputs))
}

func resultText(cluster, uc string, inputs map[string]string, res core.RunResult) string {
	if res.Err != nil {
		return fmt.Sprintf("❌ <b>%s</b> on <b>%s</b>%s\n%s", esc(uc), esc(cluster), inputsLine(inputs), esc(res.Err.Error()))
	}
	verdict := "❔"
	if res.Healthy != nil {
		if *res.Healthy {
			verdict = "✅"
		} else {
			verdict = "🔴"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> on <b>%s</b>%s\n", verdict, esc(uc), esc(cluster), inputsLine(inputs))
	if res.Headline != "" {
		fmt.Fprintf(&b, "<b>%s</b>\n", esc(res.Headline))
	}
	if s := strings.TrimSpace(res.Summary); s != "" {
		b.WriteString(esc(s))
	}
	if res.Warning != "" {
		fmt.Fprintf(&b, "\n⚠️ %s", esc(res.Warning))
	}
	return b.String()
}

func errorText(msg string) string { return "⚠️ " + esc(msg) }

func inputsLine(inputs map[string]string) string {
	if len(inputs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+inputs[k])
	}
	return "\n<i>" + esc(strings.Join(parts, " ")) + "</i>"
}

// chunk4096 splits s into pieces no longer than Telegram's message limit,
// preferring to break on a newline near the boundary.
func chunk4096(s string) []string {
	if len(s) <= tgMaxMessage {
		return []string{s}
	}
	var out []string
	for len(s) > tgMaxMessage {
		cut := tgMaxMessage
		if nl := strings.LastIndexByte(s[:tgMaxMessage], '\n'); nl > tgMaxMessage/2 {
			cut = nl + 1
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
```

Note on `chunk4096` + HTML: chunking may split an HTML tag or `&amp;` entity across a boundary. Because summaries are escaped plain text with only whole `<b>`/`<i>` wrappers around short fields (never around the long `Summary` body), a mid-body cut lands inside escaped text, not a tag. The renderer (Task 10) sends only the FIRST chunk with parse mode HTML and any continuation chunks as plain text (no parse mode) to avoid a broken-entity error — see that task.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/telegram/ -run 'KB|PromptText|ResultText|Chunk' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/telegram/keyboards.go internal/platform/telegram/keyboards_test.go
git commit -m "feat: add Telegram message + inline-keyboard builders with HTML escaping"
```

---

## Task 8: Wizard session store

In-memory, TTL'd store keyed by `(chatID, messageID)` with a `(chatID, userID) → messageID` reverse index (see spec's session-store section).

**Files:**
- Create: `internal/platform/telegram/session.go`
- Test: `internal/platform/telegram/session_test.go`

**Interfaces:**
- Produces:
  - `type pending struct { user int64; cluster, usecase string; collected map[string]string; next string; deadline time.Time }`
  - `type sessions struct { ... }` with `func newSessions(ttl time.Duration) *sessions`
  - `func (*sessions) begin(chat int64, msg int, user int64, cluster, usecase string)` — dispatcher, on PickUseCase.
  - `func (*sessions) byMsg(chat int64, msg int) (*pending, bool)` — renderer.
  - `func (*sessions) byUser(chat, user int64) (msg int, p *pending, ok bool)` — dispatcher, on text reply.
  - `func (*sessions) update(chat int64, msg int, collected map[string]string, next string)` — renderer, per RenderForm.
  - `func (*sessions) end(chat int64, msg int)` — renderer on result/error; dispatcher on /cancel.
  - `func (*sessions) sweep(now time.Time)` — TTL eviction (called from a ticker in the adapter).

- [ ] **Step 1: Write the failing test**

Create `internal/platform/telegram/session_test.go`:

```go
package telegram

import (
	"testing"
	"time"
)

func TestSessionBeginFindEnd(t *testing.T) {
	s := newSessions(10 * time.Minute)
	s.begin(100, 5, 42, "prod", "deploy-check")

	p, ok := s.byMsg(100, 5)
	if !ok || p.cluster != "prod" || p.usecase != "deploy-check" || p.user != 42 {
		t.Fatalf("byMsg wrong: %#v ok=%v", p, ok)
	}

	msg, p2, ok := s.byUser(100, 42)
	if !ok || msg != 5 || p2 != p {
		t.Fatalf("byUser wrong: msg=%d ok=%v", msg, ok)
	}

	s.update(100, 5, map[string]string{"namespace": "payments"}, "deployment")
	p, _ = s.byMsg(100, 5)
	if p.collected["namespace"] != "payments" || p.next != "deployment" {
		t.Fatalf("update not applied: %#v", p)
	}

	s.end(100, 5)
	if _, ok := s.byMsg(100, 5); ok {
		t.Fatal("byMsg after end should miss")
	}
	if _, _, ok := s.byUser(100, 42); ok {
		t.Fatal("byUser after end should miss (reverse index cleaned)")
	}
}

func TestSessionSweepEvictsExpired(t *testing.T) {
	s := newSessions(1 * time.Minute)
	s.begin(1, 1, 1, "c", "u")
	s.sweep(time.Now().Add(2 * time.Minute))
	if _, ok := s.byMsg(1, 1); ok {
		t.Fatal("expired session should be swept")
	}
	if _, _, ok := s.byUser(1, 1); ok {
		t.Fatal("reverse index should be swept too")
	}
}

func TestSessionOnePerUserReplacesOld(t *testing.T) {
	s := newSessions(10 * time.Minute)
	s.begin(1, 10, 7, "c", "u1")
	s.begin(1, 20, 7, "c", "u2") // same user starts a new flow
	if _, ok := s.byMsg(1, 10); ok {
		t.Fatal("old session for the user should be dropped")
	}
	msg, p, ok := s.byUser(1, 7)
	if !ok || msg != 20 || p.usecase != "u2" {
		t.Fatalf("reverse index should point to newest: msg=%d %#v", msg, p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/telegram/ -run TestSession -v`
Expected: FAIL — `sessions` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/session.go`:

```go
package telegram

import (
	"sync"
	"time"
)

// pending is one in-flight input wizard.
type pending struct {
	user      int64
	cluster   string
	usecase   string
	collected map[string]string
	next      string // the input currently being prompted; "" when none
	deadline  time.Time
}

type msgKey struct {
	chat int64
	msg  int
}
type userKey struct {
	chat int64
	user int64
}

// sessions holds wizard state keyed by (chat, message) with a (chat, user)
// reverse index. Safe for concurrent use — go-telegram/bot dispatches updates
// on separate goroutines.
type sessions struct {
	mu     sync.Mutex
	byM    map[msgKey]*pending
	byU    map[userKey]msgKey
	ttl    time.Duration
}

func newSessions(ttl time.Duration) *sessions {
	return &sessions{byM: map[msgKey]*pending{}, byU: map[userKey]msgKey{}, ttl: ttl}
}

func (s *sessions) begin(chat int64, msg int, user int64, cluster, usecase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop any previous flow for this user (one active wizard per user per chat).
	uk := userKey{chat, user}
	if old, ok := s.byU[uk]; ok {
		delete(s.byM, old)
	}
	mk := msgKey{chat, msg}
	s.byM[mk] = &pending{
		user: user, cluster: cluster, usecase: usecase,
		collected: map[string]string{}, deadline: time.Now().Add(s.ttl),
	}
	s.byU[uk] = mk
}

func (s *sessions) byMsg(chat int64, msg int) (*pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byM[msgKey{chat, msg}]
	return p, ok
}

func (s *sessions) byUser(chat, user int64) (int, *pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk, ok := s.byU[userKey{chat, user}]
	if !ok {
		return 0, nil, false
	}
	return mk.msg, s.byM[mk], true
}

func (s *sessions) update(chat int64, msg int, collected map[string]string, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byM[msgKey{chat, msg}]; ok {
		cp := make(map[string]string, len(collected))
		for k, v := range collected {
			cp[k] = v
		}
		p.collected = cp
		p.next = next
		p.deadline = time.Now().Add(s.ttl)
	}
}

func (s *sessions) end(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk := msgKey{chat, msg}
	if p, ok := s.byM[mk]; ok {
		delete(s.byU, userKey{chat, p.user})
		delete(s.byM, mk)
	}
}

func (s *sessions) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for mk, p := range s.byM {
		if now.After(p.deadline) {
			delete(s.byU, userKey{mk.chat, p.user})
			delete(s.byM, mk)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/telegram/ -run TestSession -race -v`
Expected: PASS (race detector clean).

- [ ] **Step 5: Commit**

```bash
git add internal/platform/telegram/session.go internal/platform/telegram/session_test.go
git commit -m "feat: add Telegram wizard session store (msg-keyed + user reverse index)"
```

---

## Task 9: Group reporter (`core.GroupReporter`, edit-in-place)

Implement `core.GroupReporter` for Telegram: send one progress message, edit it in place as targets land, edit to the final tally on finish.

**Files:**
- Create: `internal/platform/telegram/groupreporter.go`
- Test: `internal/platform/telegram/groupreporter_test.go`

**Interfaces:**
- Consumes: `groupSender` (Task 5), `core.Group`/`core.GroupSummary`/`core.ServiceResult`/`core.GroupDest`.
- Produces: `type tgGroupReporter struct { s groupSender; chat int64; msgID int; ... Results []core.ServiceResult }`; `func newGroupReporter(s groupSender, chat int64) *tgGroupReporter`. Satisfies `core.GroupReporter`.

- [ ] **Step 1: Write the failing test**

Create `internal/platform/telegram/groupreporter_test.go`:

```go
package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/gopaytech/kato-bot/internal/core"
)

// fakeGroupSender records send/edit calls.
type fakeGroupSender struct {
	sent  int
	edits int
	last  string
	nextID int
}

func (f *fakeGroupSender) Send(_ context.Context, _ int64, html string, _ *models.InlineKeyboardMarkup) (int, error) {
	f.sent++
	f.last = html
	f.nextID++
	return f.nextID, nil
}
func (f *fakeGroupSender) Edit(_ context.Context, _ int64, _ int, html string, _ *models.InlineKeyboardMarkup) error {
	f.edits++
	f.last = html
	return nil
}

func TestGroupReporterLifecycle(t *testing.T) {
	f := &fakeGroupSender{}
	r := newGroupReporter(f, 100)
	ctx := context.Background()
	g := core.Group{Name: "critical", Cluster: "prod"}

	if err := r.Start(ctx, g, core.GroupDest{}, 2); err != nil {
		t.Fatal(err)
	}
	if f.sent != 1 {
		t.Fatalf("Start should send one progress message, got %d", f.sent)
	}
	h := true
	_ = r.ServiceDone(ctx, g, core.ServiceResult{UseCase: "uc", Healthy: &h})
	_ = r.ServiceDone(ctx, g, core.ServiceResult{UseCase: "uc", Err: errBoom{}})
	if len(r.Results) != 2 {
		t.Fatalf("want 2 results captured, got %d", len(r.Results))
	}
	_ = r.Finish(ctx, core.GroupSummary{Group: g, Total: 2, Healthy: 1, Errored: 1})
	if f.edits < 1 {
		t.Fatalf("progress/finish should edit the message, got %d edits", f.edits)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/telegram/ -run TestGroupReporter -v`
Expected: FAIL — `newGroupReporter` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/groupreporter.go`:

```go
package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

// tgGroupReporter implements core.GroupReporter over Telegram: one progress
// message edited in place as targets land. GroupRunner calls these serially.
type tgGroupReporter struct {
	s     groupSender
	chat  int64
	msgID int
	summ  core.GroupSummary
	done  int

	Results []core.ServiceResult
}

func newGroupReporter(s groupSender, chat int64) *tgGroupReporter {
	return &tgGroupReporter{s: s, chat: chat}
}

func (r *tgGroupReporter) Start(ctx context.Context, g core.Group, _ core.GroupDest, total int) error {
	r.summ = core.GroupSummary{Group: g, Total: total}
	id, err := r.s.Send(ctx, r.chat, groupProgress(g, r.summ, 0, false), nil)
	if err != nil {
		return err
	}
	r.msgID = id
	return nil
}

func (r *tgGroupReporter) ServiceDone(ctx context.Context, g core.Group, sr core.ServiceResult) error {
	r.done++
	r.Results = append(r.Results, sr)
	switch sr.Bucket() {
	case "healthy":
		r.summ.Healthy++
	case "unhealthy":
		r.summ.Unhealthy++
	case "errored":
		r.summ.Errored++
	default:
		r.summ.Unknown++
	}
	return r.s.Edit(ctx, r.chat, r.msgID, groupProgress(g, r.summ, r.done, false), nil)
}

func (r *tgGroupReporter) Finish(ctx context.Context, s core.GroupSummary) error {
	return r.s.Edit(ctx, r.chat, r.msgID, groupProgress(s.Group, s, s.Total, true), nil)
}

// groupProgress renders the parent progress/summary line.
func groupProgress(g core.Group, s core.GroupSummary, done int, final bool) string {
	var b strings.Builder
	head := "⏳"
	if final {
		head = "✅"
	}
	fmt.Fprintf(&b, "%s <b>Group %s</b> on <b>%s</b> — %d/%d\n", head, esc(g.Name), esc(g.Cluster), done, s.Total)
	fmt.Fprintf(&b, "healthy %d · unhealthy %d · errored %d · unknown %d",
		s.Healthy, s.Unhealthy, s.Errored, s.Unknown)
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/telegram/ -run TestGroupReporter -v`
Expected: PASS.

- [ ] **Step 5: Confirm it satisfies the interface**

Add to `internal/platform/telegram/groupreporter.go`:

```go
var _ core.GroupReporter = (*tgGroupReporter)(nil)
```

Run: `go build ./...`
Expected: OK.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/telegram/groupreporter.go internal/platform/telegram/groupreporter_test.go
git commit -m "feat: add Telegram group reporter (edit-in-place progress)"
```

---

## Task 10: Renderer (`core.Renderer`) + the wizard

Implement all seven `core.Renderer` methods over the `sender` + session store. `RenderForm` is the wizard: prompt for the next missing input, or show a Run button when nothing is missing. `RenderResult`/`RenderError` end the session.

**Files:**
- Create: `internal/platform/telegram/render.go`
- Test: `internal/platform/telegram/render_test.go`

**Interfaces:**
- Consumes: `sender` (Task 5), `*sessions` (Task 8), all keyboard/text builders (Task 7), `core.Reply`/`core.Contract`/etc. `Reply.ChatID` and `Reply.MessageID` are decimal strings the renderer parses to int64/int.
- Produces: `type Renderer struct { S sender; sess *sessions }`; satisfies `core.Renderer`. Helper `func requiredMissing(c core.Contract, have map[string]string) []string` (local copy of core's private missing-required logic).

- [ ] **Step 1: Write the failing tests**

Create `internal/platform/telegram/render_test.go`:

```go
package telegram

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/gopaytech/kato-bot/internal/core"
)

// fakeSender records the last Send/Edit and returns incrementing ids.
type fakeSender struct {
	sends   []recorded
	edits   []recorded
	nextID  int
	answers int
}
type recorded struct {
	chat int64
	msg  int
	html string
}

func (f *fakeSender) Send(_ context.Context, chat int64, html string, _ *models.InlineKeyboardMarkup) (int, error) {
	f.nextID++
	f.sends = append(f.sends, recorded{chat, f.nextID, html})
	return f.nextID, nil
}
func (f *fakeSender) Edit(_ context.Context, chat int64, msg int, html string, _ *models.InlineKeyboardMarkup) error {
	f.edits = append(f.edits, recorded{chat, msg, html})
	return nil
}
func (f *fakeSender) Answer(_ context.Context, _ string) error { f.answers++; return nil }

func newRenderer() (*Renderer, *fakeSender, *sessions) {
	f := &fakeSender{}
	s := newSessions(10 * time.Minute)
	return &Renderer{S: f, sess: s}, f, s
}

func reply(chat int64, msg int, cluster string) core.Reply {
	r := core.Reply{ChatID: strconv.FormatInt(chat, 10), Cluster: cluster}
	if msg != 0 {
		r.MessageID = strconv.Itoa(msg)
	}
	return r
}

func TestRenderClusterPickerSendsWhenNoMessageID(t *testing.T) {
	r, f, _ := newRenderer()
	if err := r.RenderClusterPicker(context.Background(), reply(100, 0, ""), []core.Cluster{{Name: "prod"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.sends) != 1 || len(f.edits) != 0 {
		t.Fatalf("cluster picker with no message id should Send once: sends=%d edits=%d", len(f.sends), len(f.edits))
	}
}

func TestRenderFormWithInputsPromptsAndStartsSession(t *testing.T) {
	r, f, s := newRenderer()
	// Simulate the dispatcher having begun the session at message 5 for user 42.
	s.begin(100, 5, 42, "prod", "deploy-check")
	c := core.Contract{Name: "deploy-check", Inputs: []core.InputDecl{{Name: "namespace", Required: true}}}

	if err := r.RenderForm(context.Background(), reply(100, 5, "prod"), c, nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.edits) != 1 {
		t.Fatalf("RenderForm should edit the wizard message once, got %d", len(f.edits))
	}
	p, _ := s.byMsg(100, 5)
	if p.next != "namespace" {
		t.Fatalf("session.next should be the first missing input, got %q", p.next)
	}
}

func TestRenderFormZeroInputsShowsRunButton(t *testing.T) {
	r, f, s := newRenderer()
	s.begin(100, 5, 42, "prod", "noinput")
	c := core.Contract{Name: "noinput"} // no inputs

	if err := r.RenderForm(context.Background(), reply(100, 5, "prod"), c, nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.edits) != 1 {
		t.Fatalf("want one edit for the run-button message, got %d", len(f.edits))
	}
	p, _ := s.byMsg(100, 5)
	if p.next != "" {
		t.Fatalf("zero-input session should have no pending input, got %q", p.next)
	}
}

func TestRenderResultEndsSession(t *testing.T) {
	r, _, s := newRenderer()
	s.begin(100, 5, 42, "prod", "deploy-check")
	h := true
	err := r.RenderResult(context.Background(), reply(100, 5, "prod"), "deploy-check",
		map[string]string{}, core.RunResult{Healthy: &h, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.byMsg(100, 5); ok {
		t.Fatal("RenderResult should end the session")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/platform/telegram/ -run TestRender -v`
Expected: FAIL — `Renderer` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/render.go`:

```go
package telegram

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/gopaytech/kato-bot/internal/core"
)

// Renderer implements core.Renderer for Telegram over a sender + session store.
type Renderer struct {
	S    sender
	sess *sessions
}

var _ core.Renderer = (*Renderer)(nil)

func parseChat(r core.Reply) int64 { n, _ := strconv.ParseInt(r.ChatID, 10, 64); return n }
func parseMsg(r core.Reply) int    { n, _ := strconv.Atoi(r.MessageID); return n }

// emit sends a new message (no MessageID yet) or edits the existing one.
func (rd *Renderer) emit(ctx context.Context, r core.Reply, html string, kb *models.InlineKeyboardMarkup) error {
	chat := parseChat(r)
	if r.MessageID == "" {
		_, err := rd.S.Send(ctx, chat, html, kb)
		return err
	}
	return rd.S.Edit(ctx, chat, parseMsg(r), html, kb)
}

func (rd *Renderer) RenderClusterPicker(ctx context.Context, r core.Reply, clusters []core.Cluster) error {
	text, kb := clusterPickerKB(clusters)
	return rd.emit(ctx, r, text, kb)
}

func (rd *Renderer) RenderPicker(ctx context.Context, r core.Reply, ucs []core.UseCase, groups []core.Group) error {
	text, kb := pickerKB(r.Cluster, ucs, groups)
	return rd.emit(ctx, r, text, kb)
}

func (rd *Renderer) RenderGroupConfirm(ctx context.Context, r core.Reply, g core.Group) error {
	text, kb := groupConfirmKB(g)
	return rd.emit(ctx, r, text, kb)
}

// RenderForm is the wizard step. With missing inputs it prompts for the next one
// and records it in the session; with none missing (a zero-input usecase) it
// shows a Run button. The dispatcher has already begun the session at this
// message id (on PickUseCase); on a re-render after a submit we update it.
func (rd *Renderer) RenderForm(ctx context.Context, r core.Reply, c core.Contract, prefill map[string]string, formErr string) error {
	chat, msg := parseChat(r), parseMsg(r)
	missing := requiredMissing(c, prefill)
	if len(missing) > 0 {
		next := missing[0]
		rd.sess.update(chat, msg, prefill, next)
		return rd.S.Edit(ctx, chat, msg, promptText(r.Cluster, c.Name, next, formErr), nil)
	}
	rd.sess.update(chat, msg, prefill, "")
	text, kb := runButtonKB(r.Cluster, c.Name)
	return rd.S.Edit(ctx, chat, msg, text, kb)
}

func (rd *Renderer) RenderRunning(ctx context.Context, r core.Reply, uc string, inputs map[string]string) error {
	return rd.emit(ctx, r, runningText(r.Cluster, uc, inputs), nil)
}

func (rd *Renderer) RenderResult(ctx context.Context, r core.Reply, uc string, inputs map[string]string, res core.RunResult) error {
	chat, msg := parseChat(r), parseMsg(r)
	rd.sess.end(chat, msg)
	parts := chunk4096(resultText(r.Cluster, uc, inputs, res))
	// First chunk edits the wizard message with HTML; continuations are appended
	// as plain messages (no parse mode) so a mid-body cut can't break an entity.
	if err := rd.S.Edit(ctx, chat, msg, parts[0], nil); err != nil {
		return err
	}
	for _, p := range parts[1:] {
		if _, err := rd.S.Send(ctx, chat, p, nil); err != nil {
			return err
		}
	}
	return nil
}

func (rd *Renderer) RenderError(ctx context.Context, r core.Reply, msg string) error {
	if r.MessageID != "" {
		rd.sess.end(parseChat(r), parseMsg(r))
	}
	return rd.emit(ctx, r, errorText(msg), nil)
}

// requiredMissing mirrors core's private missing-required check: the sorted names
// of required inputs that are absent or blank in have.
func requiredMissing(c core.Contract, have map[string]string) []string {
	var missing []string
	for _, in := range c.Inputs {
		if in.Required && strings.TrimSpace(have[in.Name]) == "" {
			missing = append(missing, in.Name)
		}
	}
	sort.Strings(missing)
	return missing
}
```

Note: continuation chunks in `RenderResult` are sent without HTML parse mode. Since `parts[0]` carries the escaped header and the start of the escaped summary, and the library's HTML parser only runs on `parts[0]`, a split mid-summary is safe. (If a future change wraps the long body in a tag, revisit this.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/platform/telegram/ -run TestRender -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/telegram/render.go internal/platform/telegram/render_test.go
git commit -m "feat: add Telegram Renderer with conversational input wizard"
```

---

## Task 11: Decode Telegram updates → intents

Small, testable structs mirroring the fields of a Telegram update we use, plus classification: is this a command/mention that starts the flow, a callback tap, or a wizard reply? Keep it pure by decoding from a minimal `update` shape the dispatcher fills from `models.Update`.

**Files:**
- Create: `internal/platform/telegram/decode.go`
- Test: `internal/platform/telegram/decode_test.go`

**Interfaces:**
- Produces:
  - `type inMessage struct { ChatID int64; ChatType string; MessageID int; UserID int64; Text string; IsCommand bool; MentionsBot bool }`
  - `type inCallback struct { ChatID int64; MessageID int; UserID int64; QueryID string; Data string }`
  - `func startsFlow(m inMessage) bool` — DM any text; group only when `IsCommand`+bot or `MentionsBot`.
  - `func isCancel(m inMessage) bool` — text is `/cancel`.
  - `func decodeCallback(cb inCallback) (core.Intent, core.Reply, error)` — decodes data (Task 6) and fills the addressing `Reply{ChatID, MessageID}` (decimal strings) + Cluster.

- [ ] **Step 1: Write the failing test**

Create `internal/platform/telegram/decode_test.go`:

```go
package telegram

import (
	"strconv"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestStartsFlow(t *testing.T) {
	// DM: any text starts.
	if !startsFlow(inMessage{ChatType: "private", Text: "hi"}) {
		t.Fatal("private message should start the flow")
	}
	// Group: plain text does NOT start.
	if startsFlow(inMessage{ChatType: "group", Text: "chatter"}) {
		t.Fatal("group chatter should not start the flow")
	}
	// Group: /command aimed at the bot starts.
	if !startsFlow(inMessage{ChatType: "group", Text: "/kato", IsCommand: true, MentionsBot: true}) {
		t.Fatal("group /kato@bot should start the flow")
	}
	// Group: @mention starts.
	if !startsFlow(inMessage{ChatType: "supergroup", Text: "@kato start", MentionsBot: true}) {
		t.Fatal("group @mention should start the flow")
	}
}

func TestDecodeCallbackFillsAddressing(t *testing.T) {
	cb := inCallback{ChatID: 100, MessageID: 5, UserID: 42, QueryID: "q", Data: cbUseCase("prod", "deploy-check")}
	intent, r, err := decodeCallback(cb)
	if err != nil {
		t.Fatal(err)
	}
	pu, ok := intent.(core.PickUseCase)
	if !ok || pu.Name != "deploy-check" {
		t.Fatalf("wrong intent: %#v", intent)
	}
	if r.ChatID != strconv.Itoa(100) || r.MessageID != strconv.Itoa(5) || r.Cluster != "prod" {
		t.Fatalf("addressing wrong: %#v", r)
	}
}

func TestIsCancel(t *testing.T) {
	if !isCancel(inMessage{Text: "/cancel"}) || isCancel(inMessage{Text: "namespace"}) {
		t.Fatal("cancel detection wrong")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/telegram/ -run 'StartsFlow|DecodeCallback|IsCancel' -v`
Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/platform/telegram/decode.go`:

```go
package telegram

import (
	"strconv"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

type inMessage struct {
	ChatID      int64
	ChatType    string // "private" | "group" | "supergroup" | "channel"
	MessageID   int
	UserID      int64
	Text        string
	IsCommand   bool // text begins with "/" and (for groups) targets this bot
	MentionsBot bool
}

type inCallback struct {
	ChatID    int64
	MessageID int
	UserID    int64
	QueryID   string
	Data      string
}

// startsFlow reports whether a received message should show the cluster picker.
// Private chats: any text. Group chats: only a bot-targeted /command or an
// @mention of the bot (Telegram privacy mode already filters most group noise).
func startsFlow(m inMessage) bool {
	if isCancel(m) {
		return false
	}
	if m.ChatType == "private" {
		return strings.TrimSpace(m.Text) != ""
	}
	return m.MentionsBot || m.IsCommand
}

func isCancel(m inMessage) bool { return strings.TrimSpace(m.Text) == "/cancel" }

// decodeCallback turns a callback tap into an intent plus the addressing Reply
// (chat + the tapped message, as decimal strings) with the selected cluster.
func decodeCallback(cb inCallback) (core.Intent, core.Reply, error) {
	intent, err := decodeCB(cb.Data)
	if err != nil {
		return nil, core.Reply{}, err
	}
	r := core.Reply{
		ChatID:    strconv.FormatInt(cb.ChatID, 10),
		MessageID: strconv.Itoa(cb.MessageID),
		Cluster:   clusterOf(intent),
	}
	return withReply(intent, r), r, nil
}

// clusterOf extracts the cluster decodeCB placed in the intent's Reply.
func clusterOf(in core.Intent) string { return replyOf(in).Cluster }

// replyOf returns the Reply carried by any intent.
func replyOf(in core.Intent) core.Reply {
	switch v := in.(type) {
	case core.PickCluster:
		return v.Reply
	case core.PickUseCase:
		return v.Reply
	case core.SubmitForm:
		return v.Reply
	case core.PickGroup:
		return v.Reply
	case core.RunGroup:
		return v.Reply
	case core.ListClusters:
		return v.Reply
	}
	return core.Reply{}
}

// withReply returns a copy of the intent with its Reply replaced by r.
func withReply(in core.Intent, r core.Reply) core.Intent {
	switch v := in.(type) {
	case core.PickCluster:
		v.Reply = r
		return v
	case core.PickUseCase:
		v.Reply = r
		return v
	case core.SubmitForm:
		v.Reply = r
		return v
	case core.PickGroup:
		v.Reply = r
		return v
	case core.RunGroup:
		v.Reply = r
		return v
	}
	return in
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/telegram/ -run 'StartsFlow|DecodeCallback|IsCancel' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/telegram/decode.go internal/platform/telegram/decode_test.go
git commit -m "feat: add Telegram update decoding + flow-start gating"
```

---

## Task 12: Dispatch + adapter (long-poll loop, routing, kickoff)

Wire the library's update loop to the pure pieces: classify each update, run the run semaphore + deferred run for `SubmitForm`, correlate wizard text replies, and kick off group runs. The `Adapter` provides `New`, `Name`, and `Start`.

**Files:**
- Create: `internal/platform/telegram/dispatch.go`
- Create: `internal/platform/telegram/adapter.go`
- Test: `internal/platform/telegram/dispatch_test.go` (pure routing + dedup; the poll loop itself is library-driven and build-verified).

**Interfaces:**
- Consumes: everything above; `platform.Deps`; `config.Config`; `github.com/go-telegram/bot`.
- Produces:
  - `type Adapter struct { token, apiBaseURL string; core *core.Core; r *Renderer; sess *sessions; deps platform.Deps; sem chan struct{}; seen *dedup }`
  - `func New(cfg config.Config, d platform.Deps) (platform.Adapter, error)` — `nil, nil` when `cfg.TelegramBotToken==""`.
  - `func (*Adapter) Name() string` → `"telegram"`.
  - `func (*Adapter) Start(ctx) error` — builds the bot, registers the default handler, starts long-poll.
  - `func (*Adapter) handleMessage(ctx, inMessage)` / `handleCallback(ctx, inCallback)` — routing (unit-tested via a seam that injects `inMessage`/`inCallback`).
  - `type dedup` — bounded `update_id` set (copy the shape from `lark/dispatch.go:52-80`).

- [ ] **Step 1: Write the failing test (routing + dedup, no library)**

Create `internal/platform/telegram/dispatch_test.go`:

```go
package telegram

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

// fakeKato is a minimal core.KatoClient for routing tests.
type fakeKato struct {
	ucs      []core.UseCase
	contract core.Contract
}

func (f fakeKato) ListUseCases(context.Context) ([]core.UseCase, error) { return f.ucs, nil }
func (f fakeKato) GetUseCase(context.Context, string) (core.Contract, error) { return f.contract, nil }
func (f fakeKato) Run(context.Context, string, map[string]string) (core.RunResult, error) {
	h := true
	return core.RunResult{Healthy: &h, Summary: "ok"}, nil
}

func newTestAdapter() (*Adapter, *fakeSender, *sessions) {
	reg := core.NewRegistry()
	reg.Add(core.Cluster{Name: "prod"}, fakeKato{
		ucs:      []core.UseCase{{Name: "deploy-check", Ready: true}},
		contract: core.Contract{Name: "deploy-check", Inputs: []core.InputDecl{{Name: "namespace", Required: true}}},
	})
	f := &fakeSender{}
	sess := newSessions(10 * time.Minute)
	r := &Renderer{S: f, sess: sess}
	c := &core.Core{Clusters: reg, Groups: core.NewGroupRegistry(), R: r}
	a := &Adapter{
		core: c, r: r, sess: sess,
		deps: platform.Deps{Clusters: reg, Groups: core.NewGroupRegistry(), MaxConcurrent: 4, RunTimeout: time.Minute},
		sem:  make(chan struct{}, 4),
		seen: &dedup{},
	}
	return a, f, sess
}

func TestHandleMessageStartsPicker(t *testing.T) {
	a, f, _ := newTestAdapter()
	a.handleMessage(context.Background(), inMessage{ChatID: 100, ChatType: "private", MessageID: 1, UserID: 42, Text: "hi"})
	if len(f.sends) != 1 {
		t.Fatalf("a DM should send a cluster picker, got %d sends", len(f.sends))
	}
}

func TestHandleWizardReplyAdvances(t *testing.T) {
	a, f, sess := newTestAdapter()
	// User picked the usecase: dispatcher begins the session and renders the form.
	a.handleCallback(context.Background(), inCallback{ChatID: 100, MessageID: 5, UserID: 42, QueryID: "q", Data: cbUseCase("prod", "deploy-check")})
	p, ok := sess.byMsg(100, 5)
	if !ok || p.next != "namespace" {
		t.Fatalf("form should prompt for namespace, got %#v ok=%v", p, ok)
	}
	editsBefore := len(f.edits)
	// User replies with the value; the run completes and the result is edited in.
	a.handleMessage(context.Background(), inMessage{ChatID: 100, ChatType: "private", MessageID: 6, UserID: 42, Text: "payments"})
	// Give the deferred run goroutine a moment.
	waitFor(t, func() bool { _, ok := sess.byMsg(100, 5); return !ok })
	if len(f.edits) <= editsBefore {
		t.Fatal("wizard reply should have driven at least one more edit (running/result)")
	}
}

func TestDedupDropsRepeatedUpdateID(t *testing.T) {
	d := &dedup{}
	if d.seen("u1") {
		t.Fatal("first sighting should be false")
	}
	if !d.seen("u1") {
		t.Fatal("second sighting should be true")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

var _ = strconv.Itoa
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/telegram/ -run 'HandleMessage|HandleWizard|Dedup' -v`
Expected: FAIL — `Adapter`/`dedup` undefined.

- [ ] **Step 3: Write dispatch routing**

Create `internal/platform/telegram/dispatch.go`:

```go
package telegram

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/summary"
)

// dedup drops repeated update_ids (Telegram can redeliver after a crash before
// the offset advances). Bounded FIFO, same shape as the Lark adapter's.
type dedup struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const dedupCap = 1024

func (d *dedup) seen(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids == nil {
		d.ids = make(map[string]struct{}, dedupCap)
	}
	if _, ok := d.ids[id]; ok {
		return true
	}
	d.ids[id] = struct{}{}
	d.order = append(d.order, id)
	if len(d.order) > dedupCap {
		delete(d.ids, d.order[0])
		d.order = d.order[1:]
	}
	return false
}

// handleMessage routes a received message: /cancel ends a wizard; an active
// wizard reply advances it; otherwise a flow-start shows the cluster picker.
func (a *Adapter) handleMessage(ctx context.Context, m inMessage) {
	if isCancel(m) {
		if msg, _, ok := a.sess.byUser(m.ChatID, m.UserID); ok {
			a.sess.end(m.ChatID, msg)
			_ = a.r.emit(ctx, core.Reply{ChatID: itoa64(m.ChatID), MessageID: strconv.Itoa(msg)}, errorText("cancelled"), nil)
		}
		return
	}
	// An active wizard reply? Correlate by (chat, user).
	if msg, p, ok := a.sess.byUser(m.ChatID, m.UserID); ok && p.next != "" {
		collected := map[string]string{}
		for k, v := range p.collected {
			collected[k] = v
		}
		collected[p.next] = m.Text
		r := core.Reply{ChatID: itoa64(m.ChatID), MessageID: strconv.Itoa(msg), Cluster: p.cluster}
		a.submit(ctx, core.SubmitForm{Reply: r, Name: p.usecase, Inputs: collected})
		return
	}
	if !startsFlow(m) {
		return
	}
	r := core.Reply{ChatID: itoa64(m.ChatID)} // no MessageID → Renderer sends a new picker
	if _, err := a.core.Handle(ctx, core.ListClusters{Reply: r}); err != nil {
		log.Printf("telegram: list clusters: %v", err)
	}
}

// handleCallback routes an inline-button tap.
func (a *Adapter) handleCallback(ctx context.Context, cb inCallback) {
	intent, r, err := decodeCallback(cb)
	if err != nil {
		log.Printf("telegram: decode callback: %v", err)
		return
	}
	switch v := intent.(type) {
	case core.PickUseCase:
		// Begin the wizard session at this message BEFORE core renders the form.
		a.sess.begin(cb.ChatID, cb.MessageID, cb.UserID, v.Reply.Cluster, v.Name)
		if _, e := a.core.Handle(ctx, v); e != nil {
			log.Printf("telegram: pick usecase: %v", e)
		}
	case core.SubmitForm: // zero-input Run button
		a.submit(ctx, v)
	case core.RunGroup:
		a.runGroup(ctx, v)
	default: // PickCluster, PickGroup
		if _, e := a.core.Handle(ctx, intent); e != nil {
			log.Printf("telegram: handle %T: %v", intent, e)
		}
	}
	_ = r
}

// submit runs a SubmitForm through core; a validated run is gated by the
// semaphore and executed in a goroutine, then its result is edited in.
func (a *Adapter) submit(ctx context.Context, in core.SubmitForm) {
	deferred, err := a.core.Handle(ctx, in)
	if err != nil {
		log.Printf("telegram: submit: %v", err)
		return
	}
	if deferred == nil {
		return // core re-prompted via RenderForm (still missing inputs)
	}
	chat, msg := parseChat(in.Reply), parseMsg(in.Reply)
	select {
	case a.sem <- struct{}{}:
		// Close the wizard input window: the form is complete and running, so a
		// stray text must not be read as another input (which would double-submit).
		a.sess.update(chat, msg, in.Inputs, "")
		go func() {
			defer func() { <-a.sem }()
			bg, cancel := context.WithTimeout(context.Background(), a.deps.RunTimeout)
			defer cancel()
			if e := deferred(bg); e != nil {
				log.Printf("telegram: run: %v", e)
			}
		}()
	default:
		a.sess.end(chat, msg)
		_ = a.r.RenderError(ctx, in.Reply, "kato is busy (too many runs in flight) — try again shortly")
	}
}

// runGroup gates and launches a group run, reporting progress by editing one
// message in place; an optional LLM summary follows.
func (a *Adapter) runGroup(ctx context.Context, v core.RunGroup) {
	g, ok := a.deps.Groups.Get(v.Name)
	if !ok {
		_ = a.r.RenderError(ctx, v.Reply, "unknown group "+v.Name)
		return
	}
	release, ok := a.deps.Runner.TryAcquire(g.Name)
	if !ok {
		_ = a.r.RenderError(ctx, v.Reply, "group "+g.Name+" is already running — wait for it to finish")
		return
	}
	chat := parseChat(v.Reply)
	go func() {
		defer release()
		to := a.deps.GroupTimeout
		if to <= 0 {
			to = 30 * time.Minute
		}
		bg, cancel := context.WithTimeout(context.Background(), to)
		defer cancel()
		reporter := newGroupReporter(a.groupSender(), chat)
		if err := a.deps.Runner.Run(bg, g, core.GroupDest{}, reporter); err != nil {
			log.Printf("telegram: group run %s: %v", g.Name, err)
			return
		}
		if v.Summary || g.Summary {
			text, warn := summary.Summarize(bg, a.deps.Summarizer, g, reporter.Results, a.deps.SummaryMaxEvidenceBytes)
			if warn != "" {
				text = "⚠️ " + warn
			}
			if _, e := a.groupSender().Send(bg, chat, "<b>Summary — "+esc(g.Name)+"</b>\n"+esc(text), nil); e != nil {
				log.Printf("telegram: group summary %s: %v", g.Name, e)
			}
		}
	}()
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
```

- [ ] **Step 4: Write the adapter (library loop + New)**

Create `internal/platform/telegram/adapter.go`:

```go
package telegram

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/gopaytech/kato-bot/internal/config"
	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

const defaultMaxConcurrentRuns = 4
const sessionTTL = 10 * time.Minute

// Adapter runs the Telegram long-poll loop and dispatches into core.
type Adapter struct {
	token      string
	apiBaseURL string
	pollTimeout time.Duration

	core *core.Core
	r    *Renderer
	sess *sessions
	deps platform.Deps

	sem  chan struct{}
	seen *dedup

	api *apiSender // set in Start once the bot exists; used by groupSender()
}

var _ platform.Adapter = (*Adapter)(nil)

// New builds the Telegram adapter, or (nil, nil) when no bot token is configured.
func New(cfg config.Config, d platform.Deps) (platform.Adapter, error) {
	if strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return nil, nil
	}
	n := d.MaxConcurrent
	if n <= 0 {
		n = defaultMaxConcurrentRuns
	}
	sess := newSessions(sessionTTL)
	a := &Adapter{
		token: cfg.TelegramBotToken, apiBaseURL: cfg.TelegramAPIBaseURL, pollTimeout: cfg.TelegramPollTimeout,
		sess: sess, deps: d, sem: make(chan struct{}, n), seen: &dedup{},
	}
	return a, nil
}

func (a *Adapter) Name() string { return "telegram" }

func (a *Adapter) groupSender() groupSender { return a.api }

// Start builds the bot, wires the renderer/core, and blocks on the long-poll
// loop until ctx is cancelled.
//
// VERIFY (go-telegram/bot API): confirmed against <PIN VERSION>.
//   - bot.New(token, bot.WithDefaultHandler(h), bot.WithServerURL(baseURL)) (*bot.Bot, error)
//   - default handler signature: func(ctx, *bot.Bot, *models.Update)
//   - b.Start(ctx) runs the long-poll loop; update.Message / update.CallbackQuery are *pointers.
//   - CallbackQuery.Message carries the container message (VERIFY the exact accessor for the pinned version;
//     recent versions expose it as a MaybeInaccessibleMessage — read .Message).
func (a *Adapter) Start(ctx context.Context) error {
	opts := []bot.Option{
		bot.WithDefaultHandler(func(hctx context.Context, _ *bot.Bot, u *models.Update) {
			a.onUpdate(hctx, u)
		}),
	}
	if u := strings.TrimSpace(a.apiBaseURL); u != "" && u != "https://api.telegram.org" {
		opts = append(opts, bot.WithServerURL(u))
	}
	b, err := bot.New(a.token, opts...)
	if err != nil {
		return err
	}
	a.api = newAPISender(b)
	a.r = &Renderer{S: a.api, sess: a.sess}
	a.core = &core.Core{Clusters: a.deps.Clusters, Groups: a.deps.Groups, R: a.r}

	// TTL sweeper for abandoned wizards.
	go a.sweepLoop(ctx)

	log.Printf("telegram adapter starting (poll timeout %s)", a.pollTimeout)
	b.Start(ctx) // blocks until ctx cancelled
	return ctx.Err()
}

func (a *Adapter) sweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.sess.sweep(now)
		}
	}
}

// onUpdate converts a models.Update into our pure in* shapes and routes it.
//
// VERIFY the field accessors below against the pinned models package.
func (a *Adapter) onUpdate(ctx context.Context, u *models.Update) {
	switch {
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		msg := cq.Message.Message // VERIFY: MaybeInaccessibleMessage.Message in recent versions
		if msg == nil {
			return
		}
		if a.seen.seen("cb:" + cq.ID) {
			return
		}
		_ = a.api.Answer(ctx, cq.ID) // stop the client spinner promptly
		a.handleCallback(ctx, inCallback{
			ChatID: msg.Chat.ID, MessageID: msg.ID, UserID: cq.From.ID,
			QueryID: cq.ID, Data: cq.Data,
		})
	case u.Message != nil:
		m := u.Message
		if a.seen.seen("msg:" + itoa64(int64(m.ID)) + ":" + itoa64(m.Chat.ID)) {
			return
		}
		var userID int64
		if m.From != nil {
			userID = m.From.ID
		}
		a.handleMessage(ctx, inMessage{
			ChatID: m.Chat.ID, ChatType: string(m.Chat.Type), MessageID: m.ID, UserID: userID,
			Text: m.Text, IsCommand: isBotCommand(m), MentionsBot: mentionsBot(m),
		})
	}
}

// isBotCommand / mentionsBot inspect entities. VERIFY the entity types/offsets
// against the pinned models package; treat any bot_command / mention entity as
// a bot-targeted trigger (Telegram privacy mode already limits what groups deliver).
func isBotCommand(m *models.Message) bool {
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeBotCommand {
			return true
		}
	}
	return false
}
func mentionsBot(m *models.Message) bool {
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeMention {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify they pass; build**

Run: `go test ./internal/platform/telegram/ -run 'HandleMessage|HandleWizard|Dedup' -race -v && go build ./...`
Expected: routing tests PASS; build OK. Fix any `models` field/method mismatches flagged by the compiler (update the VERIFY notes to the real API).

- [ ] **Step 6: Full package test + vet**

Run: `go test ./internal/platform/telegram/ -race && go vet ./internal/platform/telegram/ && gofmt -l internal/platform/telegram/`
Expected: PASS; vet clean; `gofmt -l` prints nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/platform/telegram/dispatch.go internal/platform/telegram/adapter.go internal/platform/telegram/dispatch_test.go
git commit -m "feat: add Telegram dispatch loop, wizard correlation, and group-run kickoff"
```

---

## Task 13: Wire Telegram into main.go (MILESTONE: full-parity bot)

**Files:**
- Modify: `cmd/kato-bot/main.go`

**Interfaces:**
- Consumes: `telegram.New` (Task 12), the `deps`/`NonNil` wiring from Task 3.

- [ ] **Step 1: Add telegram to the adapter list**

In `cmd/kato-bot/main.go`, add the import `"github.com/gopaytech/kato-bot/internal/platform/telegram"`, and replace the `adapters := platform.NonNil(lk)` block (from Task 3) with:

```go
	tg, err := telegram.New(cfg, deps)
	if err != nil {
		log.Fatalf("telegram init: %v", err)
	}
	adapters := platform.NonNil(lk, tg)
	if len(adapters) == 0 {
		log.Fatal("configure Lark and/or Telegram")
	}
```

- [ ] **Step 2: Build, vet, full test**

Run: `go build ./... && go vet ./... && make test`
Expected: all PASS.

- [ ] **Step 3: Smoke-run Telegram-only wiring**

Run: `TELEGRAM_BOT_TOKEN=000:invalid LARK_APP_ID= LARK_APP_SECRET= KATO_CLUSTERS_FILE=<(printf 'clusters:\n  - name: default\n    url: http://kato:8080\n') go run ./cmd/kato-bot 2>&1 | head -5`
Expected: config loads (Telegram-only accepted), the Telegram adapter starts and then fails to reach Telegram with the bogus token — proving the enable path. Ctrl-C to stop. (A real token is needed for an actual chat test.)

- [ ] **Step 4: Commit**

```bash
git add cmd/kato-bot/main.go
git commit -m "feat: run the Telegram adapter alongside Lark (credential-gated)"
```

---

## Task 14: Helm chart — Telegram Secret, values, deployment env, optional-Lark

Make the Lark Secret optional, add a Telegram Secret, mount whichever platforms are configured, and expose the Telegram env.

**Files:**
- Create: `charts/kato-bot/templates/telegram-secret.yaml`
- Modify: `charts/kato-bot/values.yaml`, `charts/kato-bot/templates/secret.yaml`, `charts/kato-bot/templates/deployment.yaml`.

**Interfaces:**
- Consumes: existing `_helpers.tpl` (`kato-bot.name`, `kato-bot.labels`).
- Produces: values `telegram.enabled`/`telegram.botToken`/`telegram.existingSecret`/`telegram.apiBaseUrl`/`telegram.pollTimeout`; the Deployment `envFrom` referencing each enabled platform's Secret.

- [ ] **Step 1: Add Telegram values**

In `charts/kato-bot/values.yaml`, after the `lark:` block, add:

```yaml
telegram:
  # -- Enable the Telegram adapter. When true, a bot token is required (inline or via existingSecret).
  enabled: false
  # -- Name of a pre-existing Secret holding TELEGRAM_BOT_TOKEN. When set, the chart
  # references it and does NOT create its own Telegram Secret (botToken ignored).
  existingSecret: ""
  # -- Telegram bot token from BotFather. Required when telegram.enabled and no existingSecret.
  botToken: ""
  # -- Telegram Bot API base URL (override only for a self-hosted Bot API server).
  apiBaseUrl: https://api.telegram.org
  # -- getUpdates long-poll timeout (Go duration).
  pollTimeout: 30s
```

Also make the Lark block's comment reflect that it's now optional: change the `lark:` heading comment note to "Lark app id. Required unless lark.existingSecret is set or only Telegram is enabled."

- [ ] **Step 2: Guard the Lark Secret on Lark being enabled**

`charts/kato-bot/templates/secret.yaml` currently always renders unless `lark.existingSecret`. Because Lark is now optional, only render it when Lark creds are provided. Replace line 1 guard:

```yaml
{{- if and (not .Values.lark.existingSecret) (or .Values.lark.appId .Values.lark.appSecret) }}
```

(Its `required` calls still enforce both keys when the block renders.)

- [ ] **Step 3: Create the Telegram Secret**

Create `charts/kato-bot/templates/telegram-secret.yaml`:

```yaml
{{- if and .Values.telegram.enabled (not .Values.telegram.existingSecret) }}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "kato-bot.name" . }}-telegram
  labels:
    {{- include "kato-bot.labels" . | nindent 4 }}
type: Opaque
stringData:
  TELEGRAM_BOT_TOKEN: {{ required "telegram.botToken is required when telegram.enabled (or set telegram.existingSecret)" .Values.telegram.botToken | quote }}
{{- end }}
```

- [ ] **Step 4: Reference the enabled platforms' Secrets via envFrom**

In `charts/kato-bot/templates/deployment.yaml`, replace the single `envFrom` block (`:32-36`) with one that includes Lark's Secret when Lark is configured and Telegram's when enabled:

```yaml
          envFrom:
            {{- if or .Values.lark.existingSecret .Values.lark.appId .Values.lark.appSecret }}
            - secretRef:
                name: {{ .Values.lark.existingSecret | default (include "kato-bot.name" .) }}
            {{- end }}
            {{- if .Values.telegram.enabled }}
            - secretRef:
                name: {{ .Values.telegram.existingSecret | default (printf "%s-telegram" (include "kato-bot.name" .)) }}
            {{- end }}
```

- [ ] **Step 5: Add Telegram env values**

In the same file's `env:` list, after the `API_ADDR` entry, add:

```yaml
            {{- if .Values.telegram.enabled }}
            - name: TELEGRAM_API_BASE_URL
              value: {{ .Values.telegram.apiBaseUrl | quote }}
            - name: TELEGRAM_POLL_TIMEOUT
              value: {{ .Values.telegram.pollTimeout | quote }}
            {{- end }}
```

(The `TELEGRAM_BOT_TOKEN` itself comes from the Secret via `envFrom`, not here.)

- [ ] **Step 6: Render-test the chart in three modes**

```bash
helm template t charts/kato-bot --set lark.appId=cli_x --set lark.appSecret=sec | grep -E 'LARK_|TELEGRAM_|kind: Secret' | head
helm template t charts/kato-bot --set lark.appId= --set lark.appSecret= --set telegram.enabled=true --set telegram.botToken=123:abc | grep -E 'TELEGRAM_|kind: Secret'
helm template t charts/kato-bot --set lark.appId=cli_x --set lark.appSecret=sec --set telegram.enabled=true --set telegram.botToken=123:abc | grep -c 'secretRef'
```

Expected: (1) Lark-only renders the Lark Secret and no Telegram env; (2) Telegram-only renders the `-telegram` Secret and TELEGRAM_* env, no Lark Secret; (3) both → two `secretRef` entries. A no-platform render (`helm template t charts/kato-bot`) should fail the Lark `required` only if its Secret block renders — with both platforms off, no Secret renders and the pod would get no creds; that misconfig is caught at runtime by `config.Load`. Note this in the chart comment.

- [ ] **Step 7: Commit**

```bash
git add charts/kato-bot/values.yaml charts/kato-bot/templates/secret.yaml charts/kato-bot/templates/telegram-secret.yaml charts/kato-bot/templates/deployment.yaml
git commit -m "feat: Helm chart supports Telegram (optional-Lark, telegram Secret + env)"
```

---

## Task 15: Docs — README env table, Telegram setup, ARCHITECTURE

**Files:**
- Modify: `charts/kato-bot/README.md.gotmpl` (regenerates both READMEs), `ARCHITECTURE.md`.

**Interfaces:** none (docs only).

- [ ] **Step 1: Add Telegram env rows + setup section to the chart README template**

In `charts/kato-bot/README.md.gotmpl`, add rows to the `## Configuration (env)` table:

```
| `TELEGRAM_BOT_TOKEN` | (none) | BotFather token; presence enables the Telegram adapter |
| `TELEGRAM_API_BASE_URL` | `https://api.telegram.org` | override for a self-hosted Bot API server |
| `TELEGRAM_POLL_TIMEOUT` | `30s` | `getUpdates` long-poll timeout |
```

Change the `LARK_APP_ID`/`LARK_APP_SECRET` rows' "(required)" to "(required for Lark)". After the "## Lark app setup" section, add:

```
## Telegram bot setup

- Create a bot with @BotFather and copy its token into `telegram.botToken` (or a
  Secret referenced by `telegram.existingSecret`) with `telegram.enabled=true`.
- DM the bot, or add it to a group. In groups, trigger it with `/kato` or by
  @mentioning it (enable group privacy off, or add it as admin, so it receives
  the trigger message). It fills use-case inputs by asking one question at a time;
  reply with each value, or send `/cancel` to abort.
- Lark and Telegram can run together in one deployment; configure either or both.
```

Update the "v1 supports Lark; Slack and Telegram are planned" line to "Supports Lark and Telegram on the same core."

- [ ] **Step 2: Regenerate the READMEs**

Run: `make readme`
Then verify: `grep -c TELEGRAM_BOT_TOKEN README.md charts/kato-bot/README.md`
Expected: each file matches (both regenerated from the template).

- [ ] **Step 3: Update ARCHITECTURE.md**

In `ARCHITECTURE.md`, update the "Extending to another platform" section (and the intro's "Lark (Feishu) chat adapter" framing) to note Telegram is now a second implemented adapter, and add a short "The Telegram adapter" subsection summarizing: long-poll transport; message-edit instead of card-patch; the conversational input wizard + in-memory session store; names-based `callback_data`; the generic `platform.Adapter` seam and per-platform `New` constructors; groups via edit-in-place progress. Keep it to ~15 lines, matching the doc's tone.

- [ ] **Step 4: Final full verification**

Run: `go build ./... && go vet ./... && make test && gofmt -l internal/ cmd/`
Expected: build/vet clean; ALL tests pass; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add charts/kato-bot/README.md.gotmpl README.md charts/kato-bot/README.md ARCHITECTURE.md
git commit -m "docs: document the Telegram adapter (README env/setup + ARCHITECTURE)"
```

---

## Milestones

- **After Task 3:** the generic `platform.Adapter` loop runs Lark with behavior unchanged (refactor complete).
- **After Task 13:** a working full-parity Telegram bot (single-run with the input wizard **and** group runs + summary), running alongside Lark.
- **After Task 15:** deployment (Helm) and docs complete.

## Self-Review notes (for the executor)

- `internal/core` / `internal/kato` are never edited — if a task tempts you to, stop: the wizard is driven by core's existing `SubmitForm→RenderForm` loop (Task 10) and needs no core change.
- Names must stay consistent across tasks: `sessions`/`pending`/`begin`/`byMsg`/`byUser`/`update`/`end`/`sweep` (Task 8); `sender`/`groupSender`/`apiSender`/`newAPISender` (Task 5); `Renderer{S, sess}` (Task 10); `Adapter{core, r, sess, deps, sem, seen, api}` (Task 12); callback funcs `cbCluster`/`cbUseCase`/`cbRun`/`cbGroup`/`cbRunGroup`/`decodeCB` (Task 6).
- Every `github.com/go-telegram/bot` call carries a `// VERIFY` note; confirm each against the pinned version during Tasks 5 and 12 and correct field/method names as needed — the pure tests don't cover the library boundary, so the compiler + a real-token smoke test are your checks there.
{% endraw %}