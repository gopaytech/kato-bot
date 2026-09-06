package telegram

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/gopaytech/kato-bot/internal/core"
)

// fakeGroupSender records send/edit calls.
type fakeGroupSender struct {
	sent   int
	edits  int
	last   string
	nextID int
}

func (f *fakeGroupSender) Send(_ context.Context, _ int64, html string, _ *models.InlineKeyboardMarkup) (int, error) {
	f.sent++
	f.last = html
	f.nextID++
	return f.nextID, nil
}
func (f *fakeGroupSender) Edit(_ context.Context, _ int64, _ int, html string, _ *models.InlineKeyboardMarkup) error {
	f.edits++
	f.last = html
	return nil
}

func TestGroupReporterLifecycle(t *testing.T) {
	f := &fakeGroupSender{}
	r := newGroupReporter(f, 100)
	ctx := context.Background()
	g := core.Group{Name: "critical", Cluster: "prod"}

	if err := r.Start(ctx, g, core.GroupDest{}, 2); err != nil {
		t.Fatal(err)
	}
	if f.sent != 1 {
		t.Fatalf("Start should send one progress message, got %d", f.sent)
	}
	h := true
	_ = r.ServiceDone(ctx, g, core.ServiceResult{UseCase: "uc", Healthy: &h})
	_ = r.ServiceDone(ctx, g, core.ServiceResult{UseCase: "uc", Err: errBoom{}})
	if len(r.Results) != 2 {
		t.Fatalf("want 2 results captured, got %d", len(r.Results))
	}
	_ = r.Finish(ctx, core.GroupSummary{Group: g, Total: 2, Healthy: 1, Errored: 1})
	if f.edits < 1 {
		t.Fatalf("progress/finish should edit the message, got %d edits", f.edits)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
