package telegram

import (
	"strconv"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestStartsFlow(t *testing.T) {
	// DM: any text starts.
	if !startsFlow(inMessage{ChatType: "private", Text: "hi"}) {
		t.Fatal("private message should start the flow")
	}
	// Group: plain text does NOT start.
	if startsFlow(inMessage{ChatType: "group", Text: "chatter"}) {
		t.Fatal("group chatter should not start the flow")
	}
	// Group: /command aimed at the bot starts.
	if !startsFlow(inMessage{ChatType: "group", Text: "/kato", IsCommand: true, MentionsBot: true}) {
		t.Fatal("group /kato@bot should start the flow")
	}
	// Group: @mention starts.
	if !startsFlow(inMessage{ChatType: "supergroup", Text: "@kato start", MentionsBot: true}) {
		t.Fatal("group @mention should start the flow")
	}
}

func TestDecodeCallbackFillsAddressing(t *testing.T) {
	cb := inCallback{ChatID: 100, MessageID: 5, UserID: 42, QueryID: "q", Data: cbUseCase("prod", "deploy-check")}
	intent, r, err := decodeCallback(cb)
	if err != nil {
		t.Fatal(err)
	}
	pu, ok := intent.(core.PickUseCase)
	if !ok || pu.Name != "deploy-check" {
		t.Fatalf("wrong intent: %#v", intent)
	}
	if r.ChatID != strconv.Itoa(100) || r.MessageID != strconv.Itoa(5) || r.Cluster != "prod" {
		t.Fatalf("addressing wrong: %#v", r)
	}
}

func TestIsCancel(t *testing.T) {
	if !isCancel(inMessage{Text: "/cancel"}) || isCancel(inMessage{Text: "namespace"}) {
		t.Fatal("cancel detection wrong")
	}
}
