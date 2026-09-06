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

var _ core.GroupReporter = (*tgGroupReporter)(nil)
