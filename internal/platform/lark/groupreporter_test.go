package lark

import (
	"context"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type fakeGroupSender struct {
	replies []string // ids replied to (first call is the parent; rest thread under it)
	patches []string // ids patched
	nextID  int
}

func (f *fakeGroupSender) ReplyID(ctx context.Context, to, card string) (string, error) {
	f.replies = append(f.replies, to)
	f.nextID++
	if len(f.replies) == 1 {
		return "parent-1", nil
	}
	return "child", nil
}
func (f *fakeGroupSender) Patch(ctx context.Context, id, card string) error {
	f.patches = append(f.patches, id)
	return nil
}

// TestGroupReporterInteractiveFlow proves the reporter is interactive-only: the
// parent card is created via ReplyID(dest.InReplyTo), each service posts a
// threaded reply under the parent, and Finish patches the parent card.
func TestGroupReporterInteractiveFlow(t *testing.T) {
	fs := &fakeGroupSender{}
	rep := newGroupReporter(fs)
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
	ctx := context.Background()

	if err := rep.Start(ctx, g, core.GroupDest{InReplyTo: "user-msg"}, 2); err != nil {
		t.Fatal(err)
	}
	tru := true
	rep.ServiceDone(ctx, g, core.ServiceResult{Target: map[string]string{"deployment": "a"}, Healthy: &tru})
	rep.ServiceDone(ctx, g, core.ServiceResult{Target: map[string]string{"deployment": "b"}, Err: &core.RunError{Msg: "x"}})
	rep.Finish(ctx, core.GroupSummary{Group: g, Total: 2, Healthy: 1, Errored: 1})

	if len(fs.replies) != 3 {
		t.Fatalf("expected 3 ReplyID calls (parent + 2 threaded), got %d: %v", len(fs.replies), fs.replies)
	}
	if fs.replies[0] != "user-msg" {
		t.Errorf("Start should ReplyID to the user msg, got %q", fs.replies[0])
	}
	for _, to := range fs.replies[1:] {
		if to != "parent-1" {
			t.Errorf("child replies must thread under the parent id, got %q", to)
		}
	}
	if len(fs.patches) == 0 || fs.patches[len(fs.patches)-1] != "parent-1" {
		t.Errorf("Finish should patch the parent card, patches=%v", fs.patches)
	}
}

func TestGroupReporterAccumulatesResults(t *testing.T) {
	fs := &fakeGroupSender{}
	rep := newGroupReporter(fs)
	g := core.Group{Name: "g"}
	rep.Start(context.Background(), g, core.GroupDest{InReplyTo: "u"}, 1)
	rep.ServiceDone(context.Background(), g, core.ServiceResult{UseCase: "dt", Target: map[string]string{"deployment": "a"}})
	if len(rep.Results) != 1 || rep.Results[0].UseCase != "dt" {
		t.Fatalf("reporter did not accumulate results: %+v", rep.Results)
	}
}
