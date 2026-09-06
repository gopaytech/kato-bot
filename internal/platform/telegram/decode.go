package telegram

import (
	"strconv"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

type inMessage struct {
	ChatID      int64
	ChatType    string // "private" | "group" | "supergroup" | "channel"
	MessageID   int
	UserID      int64
	Text        string
	IsCommand   bool // text begins with "/" and (for groups) targets this bot
	MentionsBot bool
}

type inCallback struct {
	ChatID    int64
	MessageID int
	UserID    int64
	QueryID   string
	Data      string
}

// startsFlow reports whether a received message should show the cluster picker.
// Private chats: any text. Group chats: only a bot-targeted /command or an
// @mention of the bot (Telegram privacy mode already filters most group noise).
func startsFlow(m inMessage) bool {
	if isCancel(m) {
		return false
	}
	if m.ChatType == "private" {
		return strings.TrimSpace(m.Text) != ""
	}
	return m.MentionsBot || m.IsCommand
}

func isCancel(m inMessage) bool { return strings.TrimSpace(m.Text) == "/cancel" }

// decodeCallback turns a callback tap into an intent plus the addressing Reply
// (chat + the tapped message, as decimal strings) with the selected cluster.
func decodeCallback(cb inCallback) (core.Intent, core.Reply, error) {
	intent, err := decodeCB(cb.Data)
	if err != nil {
		return nil, core.Reply{}, err
	}
	r := core.Reply{
		ChatID:    strconv.FormatInt(cb.ChatID, 10),
		MessageID: strconv.Itoa(cb.MessageID),
		Cluster:   clusterOf(intent),
	}
	return withReply(intent, r), r, nil
}

// clusterOf extracts the cluster decodeCB placed in the intent's Reply.
func clusterOf(in core.Intent) string { return replyOf(in).Cluster }

// replyOf returns the Reply carried by any intent.
func replyOf(in core.Intent) core.Reply {
	switch v := in.(type) {
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
	case core.ListClusters:
		return v.Reply
	}
	return core.Reply{}
}

// withReply returns a copy of the intent with its Reply replaced by r.
func withReply(in core.Intent, r core.Reply) core.Intent {
	switch v := in.(type) {
	case core.PickCluster:
		v.Reply = r
		return v
	case core.PickUseCase:
		v.Reply = r
		return v
	case core.SubmitForm:
		v.Reply = r
		return v
	case core.PickGroup:
		v.Reply = r
		return v
	case core.RunGroup:
		v.Reply = r
		return v
	}
	return in
}
