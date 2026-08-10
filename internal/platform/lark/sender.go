package lark

import (
	"context"
	"fmt"
	"log"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// apiSender implements sender using the Lark API client (larkim).
type apiSender struct{ cli *lark.Client }

// NewSender builds a Renderer backed by the Lark API client for the given app creds,
// targeting the given open-platform base URL (e.g. https://open.larksuite.com).
func NewSender(appID, appSecret, baseURL string) *Renderer {
	cli := lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(openBaseURL(baseURL)))
	return &Renderer{S: &apiSender{cli: cli}}
}

// openBaseURL returns the configured open-platform base URL, defaulting to the Lark
// international domain when empty.
func openBaseURL(s string) string {
	if strings.TrimSpace(s) == "" {
		return lark.LarkBaseUrl // https://open.larksuite.com
	}
	return strings.TrimSpace(s)
}

// Reply posts a new interactive card as a threaded reply to the user's message.
// ReplyInThread(true) makes the card (and the whole pick→form→result flow that follows
// on the same card) live in a thread off the user's message, keeping group chats tidy.
//
// VERIFY (larkim request shape): confirmed against oapi-sdk-go v3.9.5:
//   - larkim.NewReplyMessageReqBuilder().MessageId(id).Body(...).Build() is the correct builder chain.
//   - Body uses NewReplyMessageReqBodyBuilder().MsgType("interactive").Content(cardJSON).ReplyInThread(true).Build().
//   - cli.Im.V1.Message.Reply(ctx, req) — Im is *im.Service which embeds *v1.V1; V1.Message is *message.
//   - resp.Success() exists; resp.Msg and resp.Code come from embedded larkcore.CodeError.
func (s *apiSender) Reply(ctx context.Context, toMessageID, cardJSON string) error {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(toMessageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(cardJSON).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := s.cli.Im.V1.Message.Reply(ctx, req)
	if err != nil {
		log.Printf("lark reply transport error (to=%s): %v", toMessageID, err)
		return err
	}
	if !resp.Success() {
		log.Printf("lark reply FAILED (to=%s): code=%d msg=%s", toMessageID, resp.Code, resp.Msg)
		return fmt.Errorf("lark reply: %s (code %d)", resp.Msg, resp.Code)
	}
	log.Printf("lark reply ok (to=%s, new card sent)", toMessageID)
	return nil
}

// Patch replaces the content of an existing interactive card the bot sent.
//
// VERIFY (larkim request shape): confirmed against oapi-sdk-go v3.9.5:
//   - larkim.NewPatchMessageReqBuilder().MessageId(id).Body(...).Build() is the correct builder chain.
//   - Body uses NewPatchMessageReqBodyBuilder().Content(cardJSON).Build() (no MsgType on patch).
//   - cli.Im.V1.Message.Patch(ctx, req) — same path as Reply above.
func (s *apiSender) Patch(ctx context.Context, messageID, cardJSON string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build()
	resp, err := s.cli.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		log.Printf("lark patch transport error (msg=%s): %v", messageID, err)
		return err
	}
	if !resp.Success() {
		log.Printf("lark patch FAILED (msg=%s): code=%d msg=%s", messageID, resp.Code, resp.Msg)
		return fmt.Errorf("lark patch: %s (code %d)", resp.Msg, resp.Code)
	}
	log.Printf("lark patch ok (msg=%s)", messageID)
	return nil
}

// ReplyID mirrors Reply but returns the new message's id, so the caller can thread
// further replies (or patches) off it. Used to anchor a group run's parent card when
// the run was triggered interactively (there is a user message to reply to).
func (s *apiSender) ReplyID(ctx context.Context, toMessageID, cardJSON string) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(toMessageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(cardJSON).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := s.cli.Im.V1.Message.Reply(ctx, req)
	if err != nil {
		log.Printf("lark reply(id) transport error (to=%s): %v", toMessageID, err)
		return "", err
	}
	if !resp.Success() {
		log.Printf("lark reply(id) FAILED (to=%s): code=%d msg=%s", toMessageID, resp.Code, resp.Msg)
		return "", fmt.Errorf("lark reply: %s (code %d)", resp.Msg, resp.Code)
	}
	id := larkcore.StringValue(resp.Data.MessageId)
	log.Printf("lark reply(id) ok (to=%s, new message id=%s)", toMessageID, id)
	return id, nil
}
