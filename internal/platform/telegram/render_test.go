package telegram

import (
	"context"
	"strconv"
	"strings"
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

// noSplitEntity fails the test if body ends mid-HTML-entity: if it contains a
// '&', the last '&' must be matched by a ';' at or before the end of body.
func noSplitEntity(t *testing.T, label, body string) {
	t.Helper()
	if len(body) > 4096 {
		t.Fatalf("%s exceeds 4096 bytes: %d", label, len(body))
	}
	lastAmp := strings.LastIndexByte(body, '&')
	lastSemi := strings.LastIndexByte(body, ';')
	if lastAmp >= 0 && lastAmp > lastSemi {
		t.Fatalf("%s ends mid-entity (last '&' at %d, last ';' at %d): %q", label, lastAmp, lastSemi, body)
	}
}

func TestRenderResultMultiChunkIsHTMLSafe(t *testing.T) {
	r, f, s := newRenderer()
	s.begin(100, 5, 42, "prod", "deploy-check")

	// A long summary with '&' scattered every other byte: once HTML-escaped
	// ("&" -> "&amp;") this packs entities densely enough that at least one
	// would straddle a naive 4096-byte cut if chunk4096 weren't entity-safe.
	summary := strings.Repeat("x&", 3000)
	h := true
	err := r.RenderResult(context.Background(), reply(100, 5, "prod"), "deploy-check",
		map[string]string{}, core.RunResult{Healthy: &h, Summary: summary})
	if err != nil {
		t.Fatal(err)
	}

	if len(f.edits) != 1 {
		t.Fatalf("want exactly 1 Edit (first chunk), got %d", len(f.edits))
	}
	if len(f.sends) == 0 {
		t.Fatalf("want at least 1 Send continuation for a multi-chunk result, got 0")
	}
	if _, ok := s.byMsg(100, 5); ok {
		t.Fatal("RenderResult should end the session")
	}

	noSplitEntity(t, "edit", f.edits[0].html)
	for i, snd := range f.sends {
		noSplitEntity(t, "send["+strconv.Itoa(i)+"]", snd.html)
	}
}

func TestRenderErrorWithMessageIDEndsSessionAndEdits(t *testing.T) {
	r, f, s := newRenderer()
	s.begin(100, 5, 42, "prod", "deploy-check")

	if err := r.RenderError(context.Background(), reply(100, 5, "prod"), "boom"); err != nil {
		t.Fatal(err)
	}
	if len(f.edits) != 1 || len(f.sends) != 0 {
		t.Fatalf("RenderError with a MessageID should Edit once, not Send: edits=%d sends=%d", len(f.edits), len(f.sends))
	}
	if _, ok := s.byMsg(100, 5); ok {
		t.Fatal("RenderError with a MessageID should end the session")
	}
}

func TestRenderErrorWithoutMessageIDSendsFresh(t *testing.T) {
	r, f, s := newRenderer()
	// No session begun for this chat/message: RenderError with an empty
	// MessageID (e.g. a top-level dispatch error) must not touch it.
	s.begin(100, 5, 42, "prod", "deploy-check")

	if err := r.RenderError(context.Background(), reply(100, 0, "prod"), "boom"); err != nil {
		t.Fatal(err)
	}
	if len(f.sends) != 1 || len(f.edits) != 0 {
		t.Fatalf("RenderError with no MessageID should Send once, not Edit: sends=%d edits=%d", len(f.sends), len(f.edits))
	}
	if _, ok := s.byMsg(100, 5); !ok {
		t.Fatal("RenderError with no MessageID must not touch an unrelated session")
	}
}

func TestRenderFormWithMultipleMissingPromptsAlphabeticallyFirst(t *testing.T) {
	r, f, s := newRenderer()
	s.begin(100, 5, 42, "prod", "deploy-check")
	// Declared out of alphabetical order on purpose: core sorts missing
	// required inputs by name, and the wizard must prompt for the first one
	// alphabetically ("namespace"), not the first one declared ("zone").
	c := core.Contract{Name: "deploy-check", Inputs: []core.InputDecl{
		{Name: "zone", Required: true},
		{Name: "namespace", Required: true},
	}}

	if err := r.RenderForm(context.Background(), reply(100, 5, "prod"), c, nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.edits) != 1 {
		t.Fatalf("RenderForm should edit the wizard message once, got %d", len(f.edits))
	}
	p, _ := s.byMsg(100, 5)
	if p.next != "namespace" {
		t.Fatalf("session.next should be the alphabetically-first missing input, got %q", p.next)
	}
}
