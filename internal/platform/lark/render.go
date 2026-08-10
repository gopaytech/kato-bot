package lark

import (
	"context"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// sender is the minimal Lark message surface the renderer needs. The real impl
// (sender.go) wraps larkim; tests use a fake.
type sender interface {
	// Reply posts a new card as a reply to toMessageID (the user's message).
	Reply(ctx context.Context, toMessageID, cardJSON string) error
	// Patch replaces the card content of an existing bot message.
	Patch(ctx context.Context, messageID, cardJSON string) error
}

// groupSender is the richer outbound surface the group reporter needs: it must
// create a parent card (returning its id) and thread replies under it.
type groupSender interface {
	ReplyID(ctx context.Context, toMessageID, cardJSON string) (string, error)
	Patch(ctx context.Context, messageID, cardJSON string) error
}

// Renderer implements core.Renderer for Lark by building cards and sending/patching them.
type Renderer struct{ S sender }

// GroupSender exposes the underlying sender as a groupSender (the real *apiSender
// implements it). Panics only if wired with a sender that lacks the methods,
// which never happens in production (NewSender always uses *apiSender).
func (rd *Renderer) GroupSender() groupSender {
	return rd.S.(groupSender)
}

// emit sends a card: patch the existing bot card when r.MessageID is set, else reply
// to the user's message (the first card in a flow).
func (rd *Renderer) emit(ctx context.Context, r core.Reply, cardJSON string) error {
	if r.MessageID != "" {
		return rd.S.Patch(ctx, r.MessageID, cardJSON)
	}
	return rd.S.Reply(ctx, r.InReplyTo, cardJSON)
}

// PatchCard updates the content of an existing card message. Used to deliver the async
// run result onto the same card after the callback response has already been sent.
func (rd *Renderer) PatchCard(ctx context.Context, messageID, cardJSON string) error {
	return rd.S.Patch(ctx, messageID, cardJSON)
}

func (rd *Renderer) RenderClusterPicker(ctx context.Context, r core.Reply, clusters []core.Cluster) error {
	return rd.emit(ctx, r, buildClusterPickerCard(clusters))
}

func (rd *Renderer) RenderPicker(ctx context.Context, r core.Reply, ucs []core.UseCase, groups []core.Group) error {
	return rd.emit(ctx, r, buildPickerCard(r.Cluster, ucs, groups))
}

func (rd *Renderer) RenderGroupConfirm(ctx context.Context, r core.Reply, g core.Group) error {
	return rd.emit(ctx, r, buildGroupConfirmCard(g))
}

func (rd *Renderer) RenderForm(ctx context.Context, r core.Reply, c core.Contract, prefill map[string]string, formErr string) error {
	return rd.emit(ctx, r, buildFormCard(r.Cluster, c, prefill, formErr))
}

func (rd *Renderer) RenderRunning(ctx context.Context, r core.Reply, useCase string, inputs map[string]string) error {
	return rd.emit(ctx, r, buildRunningCard(r.Cluster, useCase, inputs))
}

func (rd *Renderer) RenderResult(ctx context.Context, r core.Reply, useCase string, inputs map[string]string, res core.RunResult) error {
	return rd.emit(ctx, r, buildResultCard(r.Cluster, useCase, inputs, res))
}

func (rd *Renderer) RenderError(ctx context.Context, r core.Reply, msg string) error {
	return rd.emit(ctx, r, buildErrorCard(msg))
}
