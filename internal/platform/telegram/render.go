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
	// Every chunk is sent under Telegram's HTML parse mode (apiSender always
	// sets ParseMode: models.ParseModeHTML, for both Edit and Send — there is
	// no plain-text path). That's fine here because chunk4096 guarantees no
	// chunk splits an HTML entity (e.g. "&amp;"), a tag (e.g. "<b>"), or a
	// UTF-8 rune across a boundary, so each chunk parses cleanly on its own.
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
	chat := parseChat(r)
	if r.MessageID == "" {
		_, err := rd.S.Send(ctx, chat, errorText(msg), nil)
		return err
	}
	mid := parseMsg(r)
	rd.sess.end(chat, mid)
	return rd.S.Edit(ctx, chat, mid, errorText(msg), nil)
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
