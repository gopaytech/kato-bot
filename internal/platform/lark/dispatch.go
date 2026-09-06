package lark

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/summary"
)

// defaultMaxConcurrentRuns bounds in-flight kato runs when MaxConcurrent is unset.
const defaultMaxConcurrentRuns = 4

// Adapter wires Lark WebSocket events into core.Core.
type Adapter struct {
	AppID         string
	AppSecret     string
	Core          *core.Core
	R             *Renderer // same instance as Core.R; used to deliver the async run result
	RunTimeout    time.Duration
	LogLevel      string // "debug" | "info" | "warn" | "error"; default info
	MaxConcurrent int    // cap on in-flight kato runs; <=0 uses defaultMaxConcurrentRuns
	BaseURL       string // open-platform base URL (e.g. https://open.larksuite.com)

	Groups       *core.GroupRegistry
	GroupRunner  *core.GroupRunner
	GroupTimeout time.Duration // per-group run budget; <=0 uses a 30-minute default

	Summarizer              summary.Client // LLM client for the group summary; nil disables it (posts a warning card)
	SummaryMaxEvidenceBytes int            // <=0 uses summary.DefaultMaxEvidenceBytes

	semOnce sync.Once
	sem     chan struct{}

	seen dedup // guards against Lark's at-least-once event redelivery
}

// Name identifies this platform for logs and the generic runner.
func (a *Adapter) Name() string { return "lark" }

// dedup drops duplicate message events. Lark delivers events at least once and REDELIVERS
// an event if it is not ACKed quickly enough; without this, a single "@kato start" can
// produce two picker cards. seen records a message id and reports whether it was already
// handled. It keeps a bounded FIFO of recent ids so memory stays flat.
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
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.ids, oldest)
	}
	return false
}

// runSem lazily builds the in-flight-run semaphore (bounded by MaxConcurrent).
func (a *Adapter) runSem() chan struct{} {
	a.semOnce.Do(func() {
		n := a.MaxConcurrent
		if n <= 0 {
			n = defaultMaxConcurrentRuns
		}
		a.sem = make(chan struct{}, n)
	})
	return a.sem
}

// larkLogLevel maps a config log-level string to the SDK's level (default info).
func larkLogLevel(s string) larkcore.LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return larkcore.LogLevelDebug
	case "warn", "warning":
		return larkcore.LogLevelWarn
	case "error":
		return larkcore.LogLevelError
	default:
		return larkcore.LogLevelInfo
	}
}

// Start opens the Lark WebSocket long-connection and dispatches events into Core.
// It blocks until ctx is cancelled.
//
// VERIFY (event field access): confirmed against oapi-sdk-go v3.9.5:
//   - OnP2MessageReceiveV1 handler receives *larkim.P2MessageReceiveV1.
//     Fields: e.Event.Message.ChatId (*string) and e.Event.Message.MessageId (*string).
//     Both are pointer fields; derefStr handles nil safely.
//   - OnP2CardActionTrigger handler receives *callback.CardActionTriggerEvent (NOT interface{}).
//     Typed fields: event.Event.Action.Value (map[string]interface{}),
//     event.Event.Action.FormValue (map[string]interface{} — note: NOT map[string]string),
//     event.Event.Context.OpenChatID (string), event.Event.Context.OpenMessageID (string).
//     The handler returns (*callback.CardActionTriggerResponse, error); we return nil response + nil error.
//     We re-marshal the typed event into the cardActionPayload JSON shape that decodeCardAction expects,
//     converting FormValue values to strings so the pure decoder remains SDK-free.
func (a *Adapter) Start(ctx context.Context) error {
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, e *larkim.P2MessageReceiveV1) error {
			// Event/Message are pointer fields in the SDK; guard against a malformed delivery.
			if e.Event == nil || e.Event.Message == nil {
				log.Printf("message event: missing Event/Message, skipping")
				return nil
			}
			msg := e.Event.Message
			chatID := derefStr(msg.ChatId)
			msgID := derefStr(msg.MessageId)
			chatType := derefStr(msg.ChatType)
			// In a group the bot must be @mentioned (e.g. "@kato start"); direct messages
			// always trigger the picker. This also guards against any unexpected delivery.
			if !shouldRespond(chatType, msg.Mentions) {
				log.Printf("event: message received (chat=%s msg=%s type=%s) — bot not @mentioned, ignoring", chatID, msgID, chatType)
				return nil
			}
			// Drop redeliveries: Lark resends an event if it is not ACKed in time, which
			// would otherwise spawn a second picker card.
			if a.seen.seen(msgID) {
				log.Printf("event: message received (msg=%s) — duplicate redelivery, ignoring", msgID)
				return nil
			}
			log.Printf("event: message received (chat=%s msg=%s type=%s) → showing picker", chatID, msgID, chatType)
			// Handle in the background so this returns NOW and Lark ACKs immediately. The
			// picker needs a kato API call plus a Lark reply; doing them inline delays the
			// ACK past Lark's window and triggers a redelivery (duplicate picker). The
			// handler ctx is cancelled once we return, so use a fresh, bounded context.
			go func() {
				bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				a.dispatch(bg, decodeMessage(chatID, msgID))
			}()
			return nil
		}).
		OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			// Event/Action/Context are pointer fields in the SDK; guard against a malformed delivery.
			if event.Event == nil || event.Event.Action == nil || event.Event.Context == nil {
				log.Printf("card action event: missing Event/Action/Context, skipping")
				return nil, nil
			}
			// Convert the typed card-action event into the JSON shape our pure decodeCardAction expects.
			// FormValue is map[string]interface{} in the SDK; stringify values for the pure decoder.
			formValueStr := make(map[string]string, len(event.Event.Action.FormValue))
			for k, v := range event.Event.Action.FormValue {
				if s, ok := v.(string); ok {
					formValueStr[k] = s
				} else {
					log.Printf("card form_value: key %q has non-string type %T, skipping", k, v)
				}
			}
			payload := map[string]any{
				"action": map[string]any{
					"value":      event.Event.Action.Value,
					"form_value": formValueStr,
				},
				"context": map[string]any{
					"open_chat_id":    event.Event.Context.OpenChatID,
					"open_message_id": event.Event.Context.OpenMessageID,
				},
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				log.Printf("card action marshal: %v", err)
				return nil, nil
			}
			intent, err := decodeCardAction(raw)
			if err != nil {
				log.Printf("card action decode: %v", err)
				return nil, nil
			}
			log.Printf("event: card action received (%T)", intent)
			// A group run executes in the adapter (not core.Handle) because it needs the
			// Lark group reporter; route it before the generic card-action path.
			if rg, ok := intent.(core.RunGroup); ok {
				return a.handleGroupRun(ctx, rg), nil
			}
			// Return the updated card in the response (type "raw") so the clicked card
			// updates inline and stays interactive.
			return a.handleCardAction(ctx, intent, replyOf(intent)), nil
		})

	cli := larkws.NewClient(a.AppID, a.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithDomain(openBaseURL(a.BaseURL)),
		larkws.WithLogLevel(larkLogLevel(a.LogLevel)),
	)
	return cli.Start(ctx)
}

// dispatch handles a message-triggered intent (the picker). It replies with a new card
// via Core's renderer. Card-action intents (pick/run) do NOT go through here — they are
// handled by handleCardAction, which returns the updated card in the callback response.
func (a *Adapter) dispatch(ctx context.Context, in core.Intent) {
	if _, err := a.Core.Handle(ctx, in); err != nil {
		log.Printf("handle %T: %v", in, err)
	}
}

// handleGroupRun resolves and gates a group run, launches it in the background, and
// returns the "started" card synchronously (the callback response). Progress/results
// are delivered later via the group reporter, threaded under reply.MessageID.
func (a *Adapter) handleGroupRun(ctx context.Context, v core.RunGroup) *callback.CardActionTriggerResponse {
	g, ok := a.Groups.Get(v.Name)
	if !ok {
		return cardResponse(buildErrorCard("unknown group " + v.Name))
	}
	release, ok := a.GroupRunner.TryAcquire(g.Name)
	if !ok {
		return cardResponse(buildErrorCard("group " + g.Name + " is already running — wait for it to finish"))
	}
	reply := v.Reply
	go func() {
		defer release()
		to := a.GroupTimeout
		if to <= 0 {
			to = 30 * time.Minute
		}
		bg, cancel := context.WithTimeout(context.Background(), to)
		defer cancel()
		reporter := newGroupReporter(a.R.GroupSender())
		// Interactive: thread the parent card under the confirm card's message.
		dest := core.GroupDest{InReplyTo: reply.MessageID}
		if err := a.GroupRunner.Run(bg, g, dest, reporter); err != nil {
			log.Printf("group run %s: %v", g.Name, err)
			return
		}
		if v.Summary || g.Summary {
			sum, warn := summary.Summarize(bg, a.Summarizer, g, reporter.Results, a.SummaryMaxEvidenceBytes)
			text := sum
			if warn != "" {
				text = "⚠️ " + warn
			}
			if _, e := a.R.GroupSender().ReplyID(bg, reply.MessageID, buildGroupSummaryCard(g, text)); e != nil {
				log.Printf("group summary reply %s: %v", g.Name, e)
			}
		}
	}()
	return cardResponse(buildGroupStartedCard(g))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// shouldRespond decides whether a received message triggers the picker. Direct messages
// (p2p) always do. Group messages ("group"/"topic_group", or any non-p2p type) only do
// when the bot is @mentioned — Lark normally only delivers @mentioned group messages to
// bots, so a non-empty Mentions list is the signal that the user invoked the bot
// (e.g. "@kato start").
func shouldRespond(chatType string, mentions []*larkim.MentionEvent) bool {
	if chatType == "p2p" {
		return true
	}
	return len(mentions) > 0
}
