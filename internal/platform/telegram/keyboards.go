package telegram

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf8"

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

// chunk4096 splits s into pieces no longer than Telegram's message limit that
// are safe to send under Telegram's HTML parse mode: every chunk is valid
// UTF-8 (never ends mid-rune) and never splits an HTML entity (e.g. "&amp;")
// or tag (e.g. "<b>") across a chunk boundary, so each chunk parses cleanly
// on its own. Prefers to break on a newline near the boundary. Chunks are
// lossless: strings.Join(chunk4096(s), "") == s.
func chunk4096(s string) []string {
	if len(s) <= tgMaxMessage {
		return []string{s}
	}
	var out []string
	for len(s) > tgMaxMessage {
		cut := tgMaxMessage
		if nl := strings.LastIndexByte(s[:tgMaxMessage], '\n'); nl > tgMaxMessage/2 {
			cut = nl + 1
		} else {
			// Hard cut with no suitable newline: snap down to the nearest
			// UTF-8 rune boundary so a chunk never ends mid-rune (Telegram
			// rejects invalid UTF-8 in sendMessage).
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			if cut == 0 {
				// Pathological input (e.g. invalid UTF-8): fall back to the
				// original hard cut rather than looping forever.
				cut = tgMaxMessage
			}
		}
		// Never split an HTML entity or tag in half: Telegram HTML-parses
		// every chunk we send (see sender.go), so a truncated "&amp;" or
		// "<b>" straddling the cut would make that send fail outright with
		// "can't parse entities" — and since the session already ended by
		// the time RenderResult chunks its output, the user would get
		// nothing at all. If the tentative cut lands inside one, back it up
		// to just before the opening '&' or '<'.
		if safe := htmlSafeCut(s, cut); safe > 0 {
			cut = safe
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// htmlSafeCut checks whether s[:cut] ends inside an unterminated HTML entity
// ("&" with no following ";" before cut) or tag ("<" with no following ">"
// before cut). If so, it returns the index of that entity's/tag's opening
// character so the caller can cut before it instead. It returns 0 when no
// adjustment is needed, or when backing up would collapse the chunk to
// nothing (which — for a 4096-byte window and realistically short entities/
// tags — should not happen).
func htmlSafeCut(s string, cut int) int {
	head := s[:cut]
	back := cut
	if amp := strings.LastIndexByte(head, '&'); amp >= 0 && strings.LastIndexByte(head, ';') < amp {
		back = amp
	}
	if lt := strings.LastIndexByte(head, '<'); lt >= 0 && strings.LastIndexByte(head, '>') < lt && lt < back {
		back = lt
	}
	if back == cut || back <= 0 {
		return 0
	}
	return back
}
