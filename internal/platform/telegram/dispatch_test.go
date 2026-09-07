package telegram

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

// syncSender wraps fakeSender (render_test.go) with a mutex. fakeSender itself has
// no internal locking — it's only ever driven synchronously in render_test.go — but
// here the wizard-reply test drives it from a background goroutine (the deferred
// run submit() launches) while the test goroutine polls for the resulting edit, so
// both the write path and the read path must share one lock to be race-free.
type syncSender struct {
	mu sync.Mutex
	f  *fakeSender
}

func (s *syncSender) Send(ctx context.Context, chat int64, html string, kb *models.InlineKeyboardMarkup) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Send(ctx, chat, html, kb)
}

func (s *syncSender) Edit(ctx context.Context, chat int64, msg int, html string, kb *models.InlineKeyboardMarkup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Edit(ctx, chat, msg, html, kb)
}

func (s *syncSender) Answer(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Answer(ctx, id)
}

func (s *syncSender) sendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.f.sends)
}

func (s *syncSender) editCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.f.edits)
}

func (s *syncSender) lastSendHTML() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.f.sends) == 0 {
		return ""
	}
	return s.f.sends[len(s.f.sends)-1].html
}

// syncGroupSender wraps fakeGroupSender (groupreporter_test.go) with a mutex,
// mirroring syncSender above: runGroup drives Send/Edit from a background
// goroutine while the test polls the resulting counts, so both the write path
// and the read path must share one lock to stay race-free.
type syncGroupSender struct {
	mu sync.Mutex
	f  *fakeGroupSender
}

func (s *syncGroupSender) Send(ctx context.Context, chat int64, html string, kb *models.InlineKeyboardMarkup) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Send(ctx, chat, html, kb)
}

func (s *syncGroupSender) Edit(ctx context.Context, chat int64, msg int, html string, kb *models.InlineKeyboardMarkup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Edit(ctx, chat, msg, html, kb)
}

func (s *syncGroupSender) counts() (sent, edits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.sent, s.f.edits
}

func (s *syncGroupSender) lastHTML() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.last
}

// fakeKato is a minimal core.KatoClient for routing tests.
type fakeKato struct {
	ucs      []core.UseCase
	contract core.Contract
}

func (f fakeKato) ListUseCases(context.Context) ([]core.UseCase, error)      { return f.ucs, nil }
func (f fakeKato) GetUseCase(context.Context, string) (core.Contract, error) { return f.contract, nil }
func (f fakeKato) Run(context.Context, string, map[string]string) (core.RunResult, error) {
	h := true
	return core.RunResult{Healthy: &h, Summary: "ok"}, nil
}

func newTestAdapter() (*Adapter, *syncSender, *sessions) {
	reg := core.NewRegistry()
	reg.Add(core.Cluster{Name: "prod"}, fakeKato{
		ucs:      []core.UseCase{{Name: "deploy-check", Ready: true}},
		contract: core.Contract{Name: "deploy-check", Inputs: []core.InputDecl{{Name: "namespace", Required: true}}},
	})
	f := &syncSender{f: &fakeSender{}}
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
	if f.sendCount() != 1 {
		t.Fatalf("a DM should send a cluster picker, got %d sends", f.sendCount())
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
	editsBefore := f.editCount()
	// User replies with the value; the run completes and the result is edited in.
	a.handleMessage(context.Background(), inMessage{ChatID: 100, ChatType: "private", MessageID: 6, UserID: 42, Text: "payments"})
	// Give the deferred run goroutine a moment. RenderResult ends the session
	// before it calls Edit, so wait for both — waiting on session-end alone
	// leaves a window where the edit hasn't landed yet.
	waitFor(t, func() bool {
		_, ok := sess.byMsg(100, 5)
		return !ok && f.editCount() > editsBefore
	})
	if f.editCount() <= editsBefore {
		t.Fatal("wizard reply should have driven at least one more edit (running/result)")
	}
}

// testGroup returns a minimal single-item group on the "prod" cluster that
// newTestAdapter's fakeKato can run (fakeKato.Run always succeeds).
func testGroup(name string) core.Group {
	return core.Group{
		Name:    name,
		Cluster: "prod",
		Items:   []core.WorkItem{{UseCase: "deploy-check", Inputs: map[string]string{"namespace": "payments"}}},
	}
}

// TestRunGroupHappyPath drives runGroup end-to-end against a real
// *core.GroupRunner (fakeKato as the cluster's backing client): the run should
// acquire the group's in-flight slot, report progress via the injected group
// sender, and — since a summary is requested but no Summarizer is configured —
// follow up with the "not configured" warning as a Summary message.
func TestRunGroupHappyPath(t *testing.T) {
	a, _, _ := newTestAdapter()
	g := testGroup("g1")
	a.deps.Groups.Add(g)
	a.deps.Runner = &core.GroupRunner{Clusters: a.deps.Clusters}
	gs := &syncGroupSender{f: &fakeGroupSender{}}
	a.gsender = gs

	a.runGroup(context.Background(), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "g1", Summary: true})

	// runGroup launches a background goroutine (release/Run/Summarize/Send);
	// wait for it to finish: Start's progress Send, one ServiceDone Edit, the
	// Finish Edit, and finally the (single-chunk) Summary Send.
	waitFor(t, func() bool {
		sent, edits := gs.counts()
		return sent >= 2 && edits >= 2
	})

	sent, edits := gs.counts()
	if sent != 2 {
		t.Fatalf("want 2 sends (progress start + summary), got %d", sent)
	}
	if edits != 2 {
		t.Fatalf("want 2 edits (service-done + finish), got %d", edits)
	}
	if last := gs.lastHTML(); !strings.Contains(last, "Summary") || !strings.Contains(last, "not configured") {
		t.Fatalf("last send should be the group summary with the not-configured warning, got %q", last)
	}

	// The group's in-flight slot must be released once the run (and its
	// summary) completes, so a second run can be acquired immediately.
	release, ok := a.deps.Runner.TryAcquire("g1")
	if !ok {
		t.Fatal("group slot should be released after the run finishes")
	}
	release()
}

// TestRunGroupAlreadyRunning verifies the in-flight gate: when the group's
// slot is already held, runGroup must render an "already running" error
// synchronously (no goroutine launched) and must not touch the group sender
// or the runner at all.
func TestRunGroupAlreadyRunning(t *testing.T) {
	a, f, _ := newTestAdapter()
	g := testGroup("g1")
	a.deps.Groups.Add(g)
	a.deps.Runner = &core.GroupRunner{Clusters: a.deps.Clusters}

	release, ok := a.deps.Runner.TryAcquire(g.Name)
	if !ok {
		t.Fatal("setup: TryAcquire should succeed the first time")
	}
	defer release()

	gs := &syncGroupSender{f: &fakeGroupSender{}}
	a.gsender = gs

	sendsBefore, editsBefore := f.sendCount(), f.editCount()
	a.runGroup(context.Background(), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "g1"})

	if got := f.sendCount(); got != sendsBefore+1 {
		t.Fatalf("already-running should RenderError via a fresh Send (no MessageID), got %d new sends", got-sendsBefore)
	}
	if got := f.editCount(); got != editsBefore {
		t.Fatalf("already-running should not edit anything, got %d new edits", got-editsBefore)
	}
	if last := f.lastSendHTML(); !strings.Contains(last, "already running") {
		t.Fatalf("error text should mention already running, got %q", last)
	}

	sent, edits := gs.counts()
	if sent != 0 || edits != 0 {
		t.Fatalf("group sender should see no activity when the group is already running, got sent=%d edits=%d", sent, edits)
	}
}

// recordingGroupSender is a groupSender that records every Send/Edit call's
// html verbatim. fakeGroupSender (groupreporter_test.go) only tracks counts
// and the single last html, which isn't enough to verify chunk sizes and
// lossless reassembly, so this test-local fake keeps the full slice instead.
// Mutex-guarded: runGroup drives Send/Edit from a background goroutine while
// the test polls the recorded slices, so both sides must share a lock to stay
// race-free.
type recordingGroupSender struct {
	mu     sync.Mutex
	sends  []string
	edits  []string
	nextID int
}

func (r *recordingGroupSender) Send(_ context.Context, _ int64, html string, _ *models.InlineKeyboardMarkup) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, html)
	r.nextID++
	return r.nextID, nil
}

func (r *recordingGroupSender) Edit(_ context.Context, _ int64, _ int, html string, _ *models.InlineKeyboardMarkup) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits = append(r.edits, html)
	return nil
}

func (r *recordingGroupSender) snapshot() (sends, edits []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sends...), append([]string(nil), r.edits...)
}

// fakeSummarizer implements summary.Client with a deterministic response long
// enough (once escaped) to exceed Telegram's 4096-byte message limit, so a
// test can drive runGroup's summary path through the actual chunk4096
// splitting rather than only the short "not configured" warning that
// TestRunGroupHappyPath exercises.
type fakeSummarizer struct{ text string }

func (f *fakeSummarizer) Complete(_ context.Context, _, _ string) (string, error) {
	return f.text, nil
}

// TestRunGroupSummaryChunking proves the summary-chunking fix end-to-end: a
// real (fake) LLM summary long enough to require multiple Telegram messages
// is chunked, every chunk fits the 4096-byte limit, and the chunks reassemble
// losslessly into exactly the escaped summary body runGroup builds.
func TestRunGroupSummaryChunking(t *testing.T) {
	a, _, _ := newTestAdapter()
	g := testGroup("g1")
	a.deps.Groups.Add(g)
	a.deps.Runner = &core.GroupRunner{Clusters: a.deps.Clusters}

	// Long enough to need several 4096-byte chunks, and includes '&'/'<' so
	// HTML-escaping is exercised too.
	longSummary := strings.Repeat("A", 9000) + " & <tag> " + strings.Repeat("B", 100)
	a.deps.Summarizer = &fakeSummarizer{text: longSummary}

	rs := &recordingGroupSender{}
	a.gsender = rs

	a.runGroup(context.Background(), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "g1", Summary: true})

	// Wait for: progress Start (1 send) + ServiceDone (1 edit) + Finish (1
	// edit) + the summary, chunked into several more sends. A 9000+ char
	// escaped summary must split into at least 3 chunks at 4096 bytes each.
	waitFor(t, func() bool {
		sends, edits := rs.snapshot()
		return len(sends) >= 4 && len(edits) >= 2
	})

	sends, edits := rs.snapshot()
	if len(edits) != 2 {
		t.Fatalf("want 2 edits (service-done + finish), got %d", len(edits))
	}
	if len(sends) < 4 {
		t.Fatalf("want the progress send plus >=3 summary chunks, got %d sends", len(sends))
	}

	// sends[0] is the progress Start message; the rest are the summary chunks.
	chunks := sends[1:]
	if len(chunks) < 3 {
		t.Fatalf("expected the long summary to be chunked into >=3 messages, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 4096 {
			t.Fatalf("chunk %d exceeds Telegram's 4096-byte limit: %d bytes", i, len(c))
		}
	}
	if !strings.Contains(chunks[0], "Summary") || !strings.Contains(chunks[0], "g1") {
		t.Fatalf("first summary chunk should carry the header, got %d bytes starting %q", len(chunks[0]), chunks[0][:80])
	}

	// Chunking must be lossless: reassembling the chunks reproduces exactly
	// the escaped "<b>Summary — g1</b>\n"+html.EscapeString(longSummary) body
	// that runGroup builds internally (mirrors chunk4096's own contract).
	wantFull := "<b>Summary — " + esc(g.Name) + "</b>\n" + esc(longSummary)
	if got := strings.Join(chunks, ""); got != wantFull {
		t.Fatalf("reassembled chunks should losslessly reproduce the escaped summary body (got len %d, want len %d)", len(got), len(wantFull))
	}

	// The group's in-flight slot must be released once the run (and its
	// summary) completes, so a second run can be acquired immediately.
	release, ok := a.deps.Runner.TryAcquire("g1")
	if !ok {
		t.Fatal("group slot should be released after the run finishes")
	}
	release()
}

// TestAllowed covers the (*Adapter).allowed predicate directly: chats/users maps
// are set on a bare test Adapter (no need to exercise the real onUpdate/bot
// library path) to verify the AND semantics between the chat and user
// dimensions, including that both empty means "allow everyone" (backward
// compatible default).
func TestAllowed(t *testing.T) {
	cases := []struct {
		name         string
		allowedChats map[int64]struct{}
		allowedUsers map[int64]struct{}
		chatID       int64
		userID       int64
		want         bool
	}{
		{
			name: "both empty allows anything",
			// no allowedChats/allowedUsers set
			chatID: -1001234567890, userID: 999,
			want: true,
		},
		{
			name:         "chats-only: matching negative group chat id, any user",
			allowedChats: map[int64]struct{}{-1001234567890: {}},
			chatID:       -1001234567890, userID: 12345,
			want: true,
		},
		{
			name:         "chats-only: non-matching chat denied regardless of user",
			allowedChats: map[int64]struct{}{-1001234567890: {}},
			chatID:       -999, userID: 12345,
			want: false,
		},
		{
			name:         "users-only: matching user in any chat",
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       777, userID: 42,
			want: true,
		},
		{
			name:         "users-only: non-matching user denied",
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       777, userID: 43,
			want: false,
		},
		{
			name:         "both set: matches both",
			allowedChats: map[int64]struct{}{-100: {}},
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       -100, userID: 42,
			want: true,
		},
		{
			name:         "both set: chat matches but user doesn't",
			allowedChats: map[int64]struct{}{-100: {}},
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       -100, userID: 43,
			want: false,
		},
		{
			name:         "both set: user matches but chat doesn't",
			allowedChats: map[int64]struct{}{-100: {}},
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       -200, userID: 42,
			want: false,
		},
		{
			name:         "both set: neither matches",
			allowedChats: map[int64]struct{}{-100: {}},
			allowedUsers: map[int64]struct{}{42: {}},
			chatID:       -200, userID: 43,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{allowedChats: tc.allowedChats, allowedUsers: tc.allowedUsers}
			if got := a.allowed(tc.chatID, tc.userID); got != tc.want {
				t.Errorf("allowed(%d, %d) = %v, want %v", tc.chatID, tc.userID, got, tc.want)
			}
		})
	}
}

// TestOnUpdateDeniedMessageDropped drives onUpdate directly (the real
// security-critical wiring, not just the allowed() predicate) with a message
// from a user outside the configured allowlist. The deny gate must return
// before handleMessage ever runs, so no cluster picker should be sent.
func TestOnUpdateDeniedMessageDropped(t *testing.T) {
	a, f, _ := newTestAdapter()
	a.allowedUsers = map[int64]struct{}{42: {}}
	a.onUpdate(context.Background(), &models.Update{Message: &models.Message{
		ID: 1, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}, From: &models.User{ID: 7}, Text: "hi",
	}})
	if got := f.sendCount(); got != 0 {
		t.Fatalf("denied message should never reach handleMessage, got %d sends", got)
	}
}

// TestOnUpdateAllowedMessagePasses is the positive counterpart: a message
// from an allowlisted user must pass the gate and reach handleMessage, which
// (private chat, non-empty text) shows the cluster picker via a single Send.
func TestOnUpdateAllowedMessagePasses(t *testing.T) {
	a, f, _ := newTestAdapter()
	a.allowedUsers = map[int64]struct{}{42: {}}
	a.onUpdate(context.Background(), &models.Update{Message: &models.Message{
		ID: 1, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}, From: &models.User{ID: 42}, Text: "hi",
	}})
	if got := f.sendCount(); got != 1 {
		t.Fatalf("allowed message should reach handleMessage and show the picker, got %d sends", got)
	}
}

// TestOnUpdateDeniedCallbackDropped exercises the callback branch of onUpdate.
// A denied callback must return before a.api.Answer (so a.api stays nil-safe
// here) and before any dispatch: no Send/Edit, and no wizard session created.
func TestOnUpdateDeniedCallbackDropped(t *testing.T) {
	a, f, sess := newTestAdapter()
	a.allowedUsers = map[int64]struct{}{42: {}}
	a.onUpdate(context.Background(), &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "q", From: models.User{ID: 7}, Data: cbCluster("prod"),
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 5, Chat: models.Chat{ID: 100}}},
	}})
	if got := f.sendCount(); got != 0 {
		t.Fatalf("denied callback should not send anything, got %d sends", got)
	}
	if got := f.editCount(); got != 0 {
		t.Fatalf("denied callback should not edit anything, got %d edits", got)
	}
	if _, _, ok := sess.byUser(100, 7); ok {
		t.Fatal("denied callback should not create a wizard session")
	}
}

// TestAllowedUnknownChat covers (*Adapter).allowedUnknownChat directly: the
// helper onUpdate falls back to when a callback's container message is
// inaccessible (chat id unknowable). A configured chat allowlist can never
// match an unknown chat, so it must fail closed regardless of the user.
func TestAllowedUnknownChat(t *testing.T) {
	cases := []struct {
		name         string
		allowedChats map[int64]struct{}
		allowedUsers map[int64]struct{}
		userID       int64
		want         bool
	}{
		{
			name:         "chats restricted denies regardless of user",
			allowedChats: map[int64]struct{}{-100: {}},
			userID:       42,
			want:         false,
		},
		{
			name:         "chats empty, user in allowlist",
			allowedUsers: map[int64]struct{}{42: {}},
			userID:       42,
			want:         true,
		},
		{
			name:   "chats empty, users empty allows everyone",
			userID: 999,
			want:   true,
		},
		{
			name:         "chats empty, users non-empty, user not in it",
			allowedUsers: map[int64]struct{}{42: {}},
			userID:       7,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{allowedChats: tc.allowedChats, allowedUsers: tc.allowedUsers}
			if got := a.allowedUnknownChat(tc.userID); got != tc.want {
				t.Errorf("allowedUnknownChat(%d) = %v, want %v", tc.userID, got, tc.want)
			}
		})
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
