package telegram

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/summary"
)

// dedup drops repeated update_ids (Telegram can redeliver after a crash before
// the offset advances). Bounded FIFO, same shape as the Lark adapter's.
type dedup struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const dedupCap = 1024

func (d *dedup) seen(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ids == nil {
		d.ids = make(map[string]struct{}, dedupCap)
	}
	if _, ok := d.ids[id]; ok {
		return true
	}
	d.ids[id] = struct{}{}
	d.order = append(d.order, id)
	if len(d.order) > dedupCap {
		delete(d.ids, d.order[0])
		d.order = d.order[1:]
	}
	return false
}

// handleMessage routes a received message: /cancel ends a wizard; an active
// wizard reply advances it; otherwise a flow-start shows the cluster picker.
func (a *Adapter) handleMessage(ctx context.Context, m inMessage) {
	if isCancel(m) {
		if msg, _, ok := a.sess.byUser(m.ChatID, m.UserID); ok {
			a.sess.end(m.ChatID, msg)
			_ = a.r.emit(ctx, core.Reply{ChatID: itoa64(m.ChatID), MessageID: strconv.Itoa(msg)}, errorText("cancelled"), nil)
		}
		return
	}
	// An active wizard reply? Correlate by (chat, user). replyContext returns a
	// lock-scoped snapshot (message id, cluster, usecase, awaited input, and a
	// COPY of collected inputs) rather than the live *pending pointer: reading
	// pending fields outside sessions' lock would race with a concurrent
	// update/end from another goroutine (go-telegram/bot dispatches updates
	// concurrently).
	if msg, cluster, usecase, next, collected, ok := a.sess.replyContext(m.ChatID, m.UserID); ok {
		collected[next] = m.Text
		r := core.Reply{ChatID: itoa64(m.ChatID), MessageID: strconv.Itoa(msg), Cluster: cluster}
		a.submit(ctx, core.SubmitForm{Reply: r, Name: usecase, Inputs: collected})
		return
	}
	if !startsFlow(m) {
		return
	}
	r := core.Reply{ChatID: itoa64(m.ChatID)} // no MessageID → Renderer sends a new picker
	if _, err := a.core.Handle(ctx, core.ListClusters{Reply: r}); err != nil {
		log.Printf("telegram: list clusters: %v", err)
	}
}

// handleCallback routes an inline-button tap.
func (a *Adapter) handleCallback(ctx context.Context, cb inCallback) {
	intent, r, err := decodeCallback(cb)
	if err != nil {
		log.Printf("telegram: decode callback: %v", err)
		return
	}
	switch v := intent.(type) {
	case core.PickUseCase:
		// Begin the wizard session at this message BEFORE core renders the form.
		a.sess.begin(cb.ChatID, cb.MessageID, cb.UserID, v.Reply.Cluster, v.Name)
		if _, e := a.core.Handle(ctx, v); e != nil {
			log.Printf("telegram: pick usecase: %v", e)
		}
	case core.SubmitForm: // zero-input Run button
		a.submit(ctx, v)
	case core.RunGroup:
		a.runGroup(ctx, v)
	default: // PickCluster, PickGroup
		if _, e := a.core.Handle(ctx, intent); e != nil {
			log.Printf("telegram: handle %T: %v", intent, e)
		}
	}
	_ = r
}

// submit runs a SubmitForm through core; a validated run is gated by the
// semaphore and executed in a goroutine, then its result is edited in.
func (a *Adapter) submit(ctx context.Context, in core.SubmitForm) {
	deferred, err := a.core.Handle(ctx, in)
	if err != nil {
		log.Printf("telegram: submit: %v", err)
		return
	}
	if deferred == nil {
		return // core re-prompted via RenderForm (still missing inputs)
	}
	chat, msg := parseChat(in.Reply), parseMsg(in.Reply)
	select {
	case a.sem <- struct{}{}:
		// Close the wizard input window: the form is complete and running, so a
		// stray text must not be read as another input (which would double-submit).
		a.sess.update(chat, msg, in.Inputs, "")
		go func() {
			defer func() { <-a.sem }()
			bg, cancel := context.WithTimeout(context.Background(), a.deps.RunTimeout)
			defer cancel()
			if e := deferred(bg); e != nil {
				log.Printf("telegram: run: %v", e)
			}
		}()
	default:
		a.sess.end(chat, msg)
		_ = a.r.RenderError(ctx, in.Reply, "kato is busy (too many runs in flight) — try again shortly")
	}
}

// runGroup gates and launches a group run, reporting progress by editing one
// message in place; an optional LLM summary follows.
func (a *Adapter) runGroup(ctx context.Context, v core.RunGroup) {
	g, ok := a.deps.Groups.Get(v.Name)
	if !ok {
		_ = a.r.RenderError(ctx, v.Reply, "unknown group "+v.Name)
		return
	}
	release, ok := a.deps.Runner.TryAcquire(g.Name)
	if !ok {
		_ = a.r.RenderError(ctx, v.Reply, "group "+g.Name+" is already running — wait for it to finish")
		return
	}
	chat := parseChat(v.Reply)
	go func() {
		defer release()
		to := a.deps.GroupTimeout
		if to <= 0 {
			to = 30 * time.Minute
		}
		bg, cancel := context.WithTimeout(context.Background(), to)
		defer cancel()
		reporter := newGroupReporter(a.groupSender(), chat)
		if err := a.deps.Runner.Run(bg, g, core.GroupDest{}, reporter); err != nil {
			log.Printf("telegram: group run %s: %v", g.Name, err)
			return
		}
		if v.Summary || g.Summary {
			text, warn := summary.Summarize(bg, a.deps.Summarizer, g, reporter.Results, a.deps.SummaryMaxEvidenceBytes)
			if warn != "" {
				text = "⚠️ " + warn
			}
			// The summary can exceed Telegram's 4096-char message limit (e.g. a long
			// LLM summary), so chunk it the same way RenderResult does. The header
			// lands in chunk 0 since we chunk the full built string. Each chunk is
			// independent: a later chunk's Send failure is logged but doesn't stop
			// earlier/subsequent chunks from being attempted.
			full := "<b>Summary — " + esc(g.Name) + "</b>\n" + esc(text)
			gs := a.groupSender()
			for _, part := range chunk4096(full) {
				if _, e := gs.Send(bg, chat, part, nil); e != nil {
					log.Printf("telegram: group summary %s: %v", g.Name, e)
				}
			}
		}
	}()
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
