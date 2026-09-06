package lark

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"

	"github.com/gopaytech/kato-bot/internal/core"
)

// captureRenderer implements core.Renderer by BUILDING the card into `card` instead of
// sending it, so the card-action handler can return it in the callback response.
type captureRenderer struct{ card string }

func (r *captureRenderer) RenderClusterPicker(_ context.Context, _ core.Reply, clusters []core.Cluster) error {
	r.card = buildClusterPickerCard(clusters)
	return nil
}
func (r *captureRenderer) RenderPicker(_ context.Context, rep core.Reply, ucs []core.UseCase, groups []core.Group) error {
	r.card = buildPickerCard(rep.Cluster, ucs, groups)
	return nil
}
func (r *captureRenderer) RenderGroupConfirm(_ context.Context, _ core.Reply, g core.Group) error {
	r.card = buildGroupConfirmCard(g)
	return nil
}
func (r *captureRenderer) RenderForm(_ context.Context, rep core.Reply, c core.Contract, prefill map[string]string, formErr string) error {
	r.card = buildFormCard(rep.Cluster, c, prefill, formErr)
	return nil
}
func (r *captureRenderer) RenderRunning(_ context.Context, rep core.Reply, uc string, in map[string]string) error {
	r.card = buildRunningCard(rep.Cluster, uc, in)
	return nil
}
func (r *captureRenderer) RenderResult(_ context.Context, rep core.Reply, uc string, in map[string]string, res core.RunResult) error {
	r.card = buildResultCard(rep.Cluster, uc, in, res)
	return nil
}
func (r *captureRenderer) RenderError(_ context.Context, _ core.Reply, msg string) error {
	r.card = buildErrorCard(msg)
	return nil
}

// cardResponse wraps a built 2.0 card as a card-action callback response so the clicked
// card updates inline and stays interactive. type MUST be "raw" for inline card JSON
// (type "card_json" causes Lark error 200672); data is the card object itself.
func cardResponse(cardJSON string) *callback.CardActionTriggerResponse {
	if cardJSON == "" {
		return nil
	}
	return &callback.CardActionTriggerResponse{
		Card: &callback.Card{Type: "raw", Data: json.RawMessage(cardJSON)},
	}
}

// replyOf extracts the Reply from any intent.
func replyOf(in core.Intent) core.Reply {
	switch v := in.(type) {
	case core.ListClusters:
		return v.Reply
	case core.PickCluster:
		return v.Reply
	case core.PickUseCase:
		return v.Reply
	case core.SubmitForm:
		return v.Reply
	case core.PickGroup:
		return v.Reply
	case core.RunGroup:
		return v.Reply
	}
	return core.Reply{}
}

// handleCardAction runs a card-action intent's synchronous logic and returns the result
// card in the callback response (updates the clicked card inline, keeps it interactive).
// A validated run is executed in a goroutine (bounded by the semaphore); when it finishes
// — after the response window — its result is patched onto the same card.
func (a *Adapter) handleCardAction(ctx context.Context, in core.Intent, reply core.Reply) *callback.CardActionTriggerResponse {
	cap := &captureRenderer{}
	tmp := &core.Core{Clusters: a.Core.Clusters, Groups: a.Core.Groups, R: cap}
	deferred, err := tmp.Handle(ctx, in)
	if err != nil {
		log.Printf("handle %T: %v", in, err)
	}
	syncCard := cap.card

	if deferred != nil {
		// A validated run. Gate BEFORE returning "running" so an over-cap submit gets "busy".
		select {
		case a.runSem() <- struct{}{}:
			go func() {
				defer func() { <-a.runSem() }()
				bg, cancel := context.WithTimeout(context.Background(), a.RunTimeout)
				_ = deferred(bg) // sets cap.card to the result card (or a form on a 400)
				cancel()         // release the run context before patching
				// Patch the result with a FRESH context: a slow run can consume the whole
				// run budget, but the result (even an error) must still reach the card.
				pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer pcancel()
				if e := a.R.PatchCard(pctx, reply.MessageID, cap.card); e != nil {
					log.Printf("result patch: %v", e)
				}
			}()
		default:
			return cardResponse(buildErrorCard("kato is busy (too many runs in flight) — try again shortly"))
		}
	}
	return cardResponse(syncCard)
}
