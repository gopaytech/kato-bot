package lark

import (
	"context"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type fakeSender struct {
	replyToMsgID string
	replyCard    string
	patchMsgID   string
	patchCard    string
	patches      int
}

func (f *fakeSender) Reply(_ context.Context, toMessageID, cardJSON string) error {
	f.replyToMsgID, f.replyCard = toMessageID, cardJSON
	return nil
}
func (f *fakeSender) Patch(_ context.Context, messageID, cardJSON string) error {
	f.patchMsgID, f.patchCard = messageID, cardJSON
	f.patches++
	return nil
}

func TestRenderPickerReplies(t *testing.T) {
	f := &fakeSender{}
	r := &Renderer{S: f}
	err := r.RenderPicker(context.Background(),
		core.Reply{ChatID: "oc", InReplyTo: "om_user"}, []core.UseCase{{Name: "a", Ready: true}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.replyToMsgID != "om_user" {
		t.Fatalf("reply to = %q", f.replyToMsgID)
	}
	if !strings.Contains(f.replyCard, `"action":"pick"`) {
		t.Errorf("picker card not sent: %s", f.replyCard)
	}
}

func TestRenderClusterPickerReplies(t *testing.T) {
	f := &fakeSender{}
	r := &Renderer{S: f}
	err := r.RenderClusterPicker(context.Background(),
		core.Reply{ChatID: "oc", InReplyTo: "om_user"}, []core.Cluster{{Name: "prod"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.replyToMsgID != "om_user" {
		t.Fatalf("reply to = %q", f.replyToMsgID)
	}
	if !strings.Contains(f.replyCard, `"action":"pick_cluster"`) {
		t.Errorf("cluster picker not sent: %s", f.replyCard)
	}
}

func TestRenderFormPatches(t *testing.T) {
	f := &fakeSender{}
	r := &Renderer{S: f}
	err := r.RenderForm(context.Background(),
		core.Reply{ChatID: "oc", MessageID: "om_card"},
		core.Contract{Name: "uc", Inputs: []core.InputDecl{{Name: "ns", Required: true}}},
		nil, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.patchMsgID != "om_card" || f.patches != 1 {
		t.Fatalf("patch msg = %q patches = %d", f.patchMsgID, f.patches)
	}
	if !strings.Contains(f.patchCard, `"action":"run"`) {
		t.Errorf("form card not patched: %s", f.patchCard)
	}
}

func TestRenderResultPatches(t *testing.T) {
	f := &fakeSender{}
	r := &Renderer{S: f}
	err := r.RenderResult(context.Background(),
		core.Reply{MessageID: "om_card"}, "uc",
		map[string]string{"ns": "p"}, core.RunResult{Phase: "Completed", Summary: "ok", Run: "uc-1"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.patchMsgID != "om_card" || !strings.Contains(f.patchCard, "ok") {
		t.Fatalf("result not patched: msg=%q card=%s", f.patchMsgID, f.patchCard)
	}
}

func TestRenderErrorPatchesWhenCardExistsElseReplies(t *testing.T) {
	// When MessageID is set (a card exists), error patches it.
	f := &fakeSender{}
	r := &Renderer{S: f}
	if err := r.RenderError(context.Background(), core.Reply{MessageID: "om_card"}, "boom"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.patches != 1 || !strings.Contains(f.patchCard, "boom") {
		t.Fatalf("expected patch with msg, got patches=%d", f.patches)
	}
	// When no card exists yet (list-time failure), error replies to the user message.
	f2 := &fakeSender{}
	r2 := &Renderer{S: f2}
	if err := r2.RenderError(context.Background(), core.Reply{InReplyTo: "om_user"}, "down"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if f2.replyToMsgID != "om_user" || !strings.Contains(f2.replyCard, "down") {
		t.Fatalf("expected reply, got %+v", f2)
	}
}

// compile-time: Renderer satisfies core.Renderer
var _ core.Renderer = (*Renderer)(nil)
