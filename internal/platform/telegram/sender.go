package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// sender is the minimal Telegram message surface the renderer needs. The real
// impl (apiSender) wraps github.com/go-telegram/bot; tests use a fake.
type sender interface {
	// Send posts a new HTML message with an optional inline keyboard; returns the
	// new message id.
	Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error)
	// Edit replaces the text + keyboard of an existing bot message.
	Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error
	// Answer acknowledges a callback query so the client stops its spinner.
	Answer(ctx context.Context, callbackQueryID string) error
}

// groupSender is the subset the group reporter needs (no callback answering).
type groupSender interface {
	Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error)
	Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error
}

// apiSender implements sender using the go-telegram/bot client.
type apiSender struct{ b *bot.Bot }

func newAPISender(b *bot.Bot) *apiSender { return &apiSender{b: b} }

// VERIFY (go-telegram/bot API): confirmed against v1.25.0 (module cache read directly:
// methods.go, methods_params.go, models/reply_markup.go, models/message.go, models/parse_mode.go).
//   - b.SendMessage(ctx, &bot.SendMessageParams{ChatID, Text, ParseMode, ReplyMarkup}) (*models.Message, error).
//   - b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID, MessageID, Text, ParseMode, ReplyMarkup}) (*models.Message, error).
//   - b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID}) (bool, error).
//   - ChatID is typed `any` on both params structs (an int64 assigns fine); models.Message.ID is `int`.
//   - ReplyMarkup is typed `models.ReplyMarkup` which is itself `any` (not a method-bearing
//     interface), so it accepts the *models.InlineKeyboardMarkup pointer directly — no
//     dereference needed. The library's own examples (examples/inline_keyboard/main.go) assign
//     the pointer straight into ReplyMarkup, so we do the same here.
//   - models.ParseModeHTML is the correct constant name (models/parse_mode.go).
func (s *apiSender) Send(ctx context.Context, chatID int64, html string, kb *models.InlineKeyboardMarkup) (int, error) {
	p := &bot.SendMessageParams{ChatID: chatID, Text: html, ParseMode: models.ParseModeHTML}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	m, err := s.b.SendMessage(ctx, p)
	if err != nil {
		return 0, err
	}
	return m.ID, nil
}

func (s *apiSender) Edit(ctx context.Context, chatID int64, messageID int, html string, kb *models.InlineKeyboardMarkup) error {
	p := &bot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: html, ParseMode: models.ParseModeHTML}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	_, err := s.b.EditMessageText(ctx, p)
	return err
}

func (s *apiSender) Answer(ctx context.Context, callbackQueryID string) error {
	_, err := s.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackQueryID})
	return err
}
