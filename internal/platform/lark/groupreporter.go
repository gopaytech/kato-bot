package lark

import (
	"context"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// groupReporter implements core.GroupReporter over Lark: a parent card plus one
// threaded reply per service, patching the parent's tallies as runs land.
// GroupRunner calls these serially, so no locking is needed.
type groupReporter struct {
	s        groupSender
	parentID string
	total    int
	done     int
	summ     core.GroupSummary
}

func newGroupReporter(s groupSender) *groupReporter { return &groupReporter{s: s} }

func (gr *groupReporter) Start(ctx context.Context, g core.Group, dest core.GroupDest, total int) error {
	gr.total = total
	gr.summ = core.GroupSummary{Group: g, Total: total}
	card := buildGroupParentCard(g, gr.summ, 0, false)
	id, err := gr.s.ReplyID(ctx, dest.InReplyTo, card)
	if err != nil {
		return err
	}
	gr.parentID = id
	return nil
}

func (gr *groupReporter) ServiceDone(ctx context.Context, g core.Group, r core.ServiceResult) error {
	gr.done++
	switch r.Bucket() {
	case "healthy":
		gr.summ.Healthy++
	case "unhealthy":
		gr.summ.Unhealthy++
	case "errored":
		gr.summ.Errored++
	default:
		gr.summ.Unknown++
	}
	// Post the service's full summary as a threaded reply under the parent.
	if _, err := gr.s.ReplyID(ctx, gr.parentID, buildServiceReplyCard(g, r)); err != nil {
		return err
	}
	// Update the parent card's progress/tallies. (One patch per service; if Lark
	// rate limits bite at ~100 targets, throttle this to every Nth call.)
	return gr.s.Patch(ctx, gr.parentID, buildGroupParentCard(g, gr.summ, gr.done, false))
}

func (gr *groupReporter) Finish(ctx context.Context, s core.GroupSummary) error {
	return gr.s.Patch(ctx, gr.parentID, buildGroupParentCard(s.Group, s, s.Total, true))
}
