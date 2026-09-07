package telegram

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/gopaytech/kato-bot/internal/config"
	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

const defaultMaxConcurrentRuns = 4
const sessionTTL = 10 * time.Minute

// Adapter runs the Telegram long-poll loop and dispatches into core.
type Adapter struct {
	token       string
	apiBaseURL  string
	pollTimeout time.Duration

	core *core.Core
	r    *Renderer
	sess *sessions
	deps platform.Deps

	sem  chan struct{}
	seen *dedup

	api     *apiSender  // the real Bot API sender; wrapped in Start and used to seed gsender
	gsender groupSender // set in Start (to api); overridable in tests via groupSender()

	// allowedChats / allowedUsers optionally restrict who can use the bot. An empty
	// (nil or zero-length) map means that dimension is unrestricted. Both empty ⇒
	// allow everyone (the historical, backward-compatible default).
	allowedChats map[int64]struct{}
	allowedUsers map[int64]struct{}
}

var _ platform.Adapter = (*Adapter)(nil)

// New builds the Telegram adapter, or (nil, nil) when no bot token is configured.
func New(cfg config.Config, d platform.Deps) (platform.Adapter, error) {
	if strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return nil, nil
	}
	n := d.MaxConcurrent
	if n <= 0 {
		n = defaultMaxConcurrentRuns
	}
	sess := newSessions(sessionTTL)
	a := &Adapter{
		token: cfg.TelegramBotToken, apiBaseURL: cfg.TelegramAPIBaseURL, pollTimeout: cfg.TelegramPollTimeout,
		sess: sess, deps: d, sem: make(chan struct{}, n), seen: &dedup{},
		allowedChats: int64Set(cfg.TelegramAllowedChats),
		allowedUsers: int64Set(cfg.TelegramAllowedUsers),
	}
	return a, nil
}

// int64Set builds a lookup set from a list of ids; a nil/empty list yields a nil map
// (so len() is 0, meaning "unrestricted" per allowed's semantics).
func int64Set(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// allowed implements the chat/user allowlist predicate: a chat (or user) dimension
// with no configured allowlist is unrestricted; both configured requires both to
// match (AND semantics). Both empty ⇒ allow everyone (backward compatible default).
func (a *Adapter) allowed(chatID, userID int64) bool {
	chatOK := len(a.allowedChats) == 0
	if !chatOK {
		_, chatOK = a.allowedChats[chatID]
	}
	userOK := len(a.allowedUsers) == 0
	if !userOK {
		_, userOK = a.allowedUsers[userID]
	}
	return chatOK && userOK
}

// allowedUnknownChat evaluates the allow predicate when the chat id could not be
// determined (an inaccessible callback-query container message). A configured chat
// allowlist can never match an unknown chat, so it fails closed (deny); with no chat
// restriction, only the user allowlist (if any) applies.
func (a *Adapter) allowedUnknownChat(userID int64) bool {
	if len(a.allowedChats) > 0 {
		return false
	}
	if len(a.allowedUsers) == 0 {
		return true
	}
	_, ok := a.allowedUsers[userID]
	return ok
}

func (a *Adapter) Name() string { return "telegram" }

// groupSender returns the sender used for group runs. It's a field (set in
// Start, or injected directly by tests) rather than always deriving from api
// so a test can supply a fake without a real *bot.Bot.
func (a *Adapter) groupSender() groupSender { return a.gsender }

// Start builds the bot, wires the renderer/core, and blocks on the long-poll
// loop until ctx is cancelled.
//
// VERIFY (go-telegram/bot API): confirmed against v1.25.0 (module cache read directly:
// bot.go, options.go, get_updates.go):
//   - bot.New(token string, options ...bot.Option) (*bot.Bot, error) — synchronously calls
//     GetMe unless bot.WithSkipGetMe() is passed, so a bad token fails New() outright.
//   - bot.WithDefaultHandler(h bot.HandlerFunc) registers the default update handler;
//     bot.HandlerFunc is `func(ctx context.Context, b *bot.Bot, update *models.Update)`.
//   - bot.WithServerURL(serverURL string) overrides the API base URL (b.url).
//   - The long-poll timeout is NOT a dedicated option: get_updates.go reads b.pollTimeout,
//     which is set only via bot.WithHTTPClient(pollTimeout time.Duration, client bot.HttpClient)
//     (bot.HttpClient is the 1-method `Do(*http.Request) (*http.Response, error)` interface,
//     satisfied by *http.Client) — passing both together is the library's only knob for this,
//     so we set the request client's own Timeout comfortably above the poll timeout.
//   - (*bot.Bot).Start(ctx context.Context) has NO return value (it blocks and logs internally
//     until ctx is cancelled) — unlike the brief's assumption of an error return.
func (a *Adapter) Start(ctx context.Context) error {
	opts := []bot.Option{
		bot.WithDefaultHandler(func(hctx context.Context, _ *bot.Bot, u *models.Update) {
			a.onUpdate(hctx, u)
		}),
	}
	if u := strings.TrimSpace(a.apiBaseURL); u != "" && u != "https://api.telegram.org" {
		opts = append(opts, bot.WithServerURL(u))
	}
	if a.pollTimeout > 0 {
		opts = append(opts, bot.WithHTTPClient(a.pollTimeout, &http.Client{Timeout: a.pollTimeout + 10*time.Second}))
	}
	b, err := bot.New(a.token, opts...)
	if err != nil {
		return err
	}
	a.api = newAPISender(b)
	a.gsender = a.api
	a.r = &Renderer{S: a.api, sess: a.sess}
	a.core = &core.Core{Clusters: a.deps.Clusters, Groups: a.deps.Groups, R: a.r}

	// TTL sweeper for abandoned wizards.
	go a.sweepLoop(ctx)

	log.Printf("telegram adapter starting (poll timeout %s)", a.pollTimeout)
	b.Start(ctx) // blocks until ctx cancelled; no error return (see VERIFY note above)
	return ctx.Err()
}

func (a *Adapter) sweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.sess.sweep(now)
		}
	}
}

// onUpdate converts a models.Update into our pure in* shapes and routes it.
//
// VERIFY the field accessors below against the pinned models package (module cache:
// models/update.go, models/callback_query.go, models/message.go, models/chat.go,
// models/user.go, models/message_entity.go), confirmed against v1.25.0:
//   - models.Update: Message *models.Message; CallbackQuery *models.CallbackQuery
//     (both pointer fields, nil-checked below).
//   - models.CallbackQuery: ID string; From models.User (VALUE, not a pointer — no nil
//     guard needed for cq.From.ID); Message models.MaybeInaccessibleMessage (also a VALUE
//     field, not *MaybeInaccessibleMessage); Data string.
//   - models.MaybeInaccessibleMessage: Message *models.Message (nil when the container
//     message is too old/deleted for Telegram to return full content — guarded below).
//   - models.Message: Chat models.Chat (value); ID int; From *models.User (pointer,
//     nil-guarded — absent for channel posts); Text string; Entities []models.MessageEntity.
//   - models.Chat: ID int64; Type models.ChatType (a defined string type — converts with
//     a plain string(...) cast, as done below).
//   - models.MessageEntity.Type is models.MessageEntityType; models.MessageEntityTypeBotCommand
//     and models.MessageEntityTypeMention are the two constants we match on.
func (a *Adapter) onUpdate(ctx context.Context, u *models.Update) {
	switch {
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		userID := cq.From.ID
		// msg.Chat.ID is the chat the callback's message lives in; cq.Message.Message
		// is nil when Telegram considers the container message inaccessible (too old
		// or deleted), in which case the chat is unknown to us.
		var (
			chatID      int64
			chatUnknown bool
			ok          bool
		)
		if cq.Message.Message != nil {
			chatID = cq.Message.Message.Chat.ID
			ok = a.allowed(chatID, userID)
		} else {
			// Fail-closed: an unknown chat can't be matched against a non-empty
			// allowlist, so any chat restriction denies it outright. With no chat
			// restriction, only the user allowlist (if any) applies.
			chatUnknown = true
			ok = a.allowedUnknownChat(userID)
		}
		if !ok {
			if chatUnknown {
				log.Printf("telegram: denied update from chat=unknown user=%d", userID)
			} else {
				log.Printf("telegram: denied update from chat=%d user=%d", chatID, userID)
			}
			return
		}
		_ = a.api.Answer(ctx, cq.ID) // stop the client spinner promptly, before any guard can return early
		msg := cq.Message.Message    // MaybeInaccessibleMessage.Message; nil when inaccessible
		if msg == nil {
			return
		}
		if a.seen.seen("cb:" + cq.ID) {
			return
		}
		a.handleCallback(ctx, inCallback{
			ChatID: msg.Chat.ID, MessageID: msg.ID, UserID: cq.From.ID,
			QueryID: cq.ID, Data: cq.Data,
		})
	case u.Message != nil:
		m := u.Message
		var userID int64
		if m.From != nil {
			userID = m.From.ID
		}
		if !a.allowed(m.Chat.ID, userID) {
			log.Printf("telegram: denied update from chat=%d user=%d", m.Chat.ID, userID)
			return
		}
		if a.seen.seen("msg:" + itoa64(int64(m.ID)) + ":" + itoa64(m.Chat.ID)) {
			return
		}
		a.handleMessage(ctx, inMessage{
			ChatID: m.Chat.ID, ChatType: string(m.Chat.Type), MessageID: m.ID, UserID: userID,
			Text: m.Text, IsCommand: isBotCommand(m), MentionsBot: mentionsBot(m),
		})
	}
}

// isBotCommand / mentionsBot inspect entities. Confirmed against v1.25.0
// (models/message_entity.go): treat any bot_command / mention entity as a
// bot-targeted trigger (Telegram privacy mode already limits what groups deliver).
func isBotCommand(m *models.Message) bool {
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeBotCommand {
			return true
		}
	}
	return false
}
func mentionsBot(m *models.Message) bool {
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeMention {
			return true
		}
	}
	return false
}
