package telegram

import (
	"context"
	"strconv"
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
