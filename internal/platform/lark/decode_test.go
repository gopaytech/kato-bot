package lark

import (
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

func TestDecodeMessageListsClusters(t *testing.T) {
	in := decodeMessage("oc_chat", "om_user_msg")
	lc, ok := in.(core.ListClusters)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if lc.Reply.ChatID != "oc_chat" || lc.Reply.InReplyTo != "om_user_msg" {
		t.Fatalf("reply = %+v", lc.Reply)
	}
}

func TestDecodeCardActionPickCluster(t *testing.T) {
	raw := []byte(`{
		"action": {"value": {"action":"pick_cluster","cluster":"prod"}},
		"context": {"open_chat_id":"oc_1","open_message_id":"om_card"}
	}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	pc, ok := in.(core.PickCluster)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if pc.Reply.Cluster != "prod" || pc.Reply.MessageID != "om_card" {
		t.Fatalf("pickcluster = %+v", pc)
	}
}

func TestDecodeCardActionPick(t *testing.T) {
	raw := []byte(`{
		"action": {"value": {"action":"pick","cluster":"prod","usecase":"pod-crashloop"}},
		"context": {"open_chat_id":"oc_1","open_message_id":"om_card"}
	}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	pick, ok := in.(core.PickUseCase)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if pick.Name != "pod-crashloop" || pick.Reply.Cluster != "prod" || pick.Reply.MessageID != "om_card" {
		t.Fatalf("pick = %+v", pick)
	}
}

func TestDecodeCardActionRun(t *testing.T) {
	raw := []byte(`{
		"action": {
			"value": {"action":"run","cluster":"prod","usecase":"pod-crashloop"},
			"form_value": {"namespace":"payments","pod":"api-xyz"}
		},
		"context": {"open_chat_id":"oc_1","open_message_id":"om_card"}
	}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	sub, ok := in.(core.SubmitForm)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if sub.Name != "pod-crashloop" || sub.Reply.Cluster != "prod" || sub.Inputs["namespace"] != "payments" {
		t.Fatalf("submit = %+v", sub)
	}
}

func TestDecodeCardActionUnknown(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"frobnicate"}},"context":{}}`)
	if _, err := decodeCardAction(raw); err == nil {
		t.Fatal("expected error on unknown action")
	}
}

func TestDecodePickGroup(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"pick_group","cluster":"prod-1","group":"critical"}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := in.(core.PickGroup)
	if !ok {
		t.Fatalf("intent = %T, want core.PickGroup", in)
	}
	if pg.Name != "critical" || pg.Reply.Cluster != "prod-1" {
		t.Errorf("bad PickGroup: %+v", pg)
	}
}

func TestDecodeRunGroup(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"run_group","cluster":"prod-1","group":"critical"}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rg, ok := in.(core.RunGroup); !ok || rg.Name != "critical" {
		t.Fatalf("intent = %T (%+v), want core.RunGroup{critical}", in, in)
	}
}

func TestDecodeRunGroupWithSummary(t *testing.T) {
	raw := []byte(`{"action":{"value":{"action":"run_group","cluster":"prod-1","group":"critical","summary":true}},"context":{"open_chat_id":"oc","open_message_id":"om"}}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatal(err)
	}
	rg, ok := in.(core.RunGroup)
	if !ok || !rg.Summary || rg.Name != "critical" {
		t.Fatalf("intent = %#v, want RunGroup{critical, Summary:true}", in)
	}
}

func TestDecodeCardActionPickClusterFromForm(t *testing.T) {
	// New card shape: the select_static delivers the choice in form_value,
	// and the submit button's value carries only the action.
	raw := []byte(`{
		"action": {"value": {"action":"pick_cluster"}, "form_value": {"cluster":"prod"}},
		"context": {"open_chat_id":"oc_1","open_message_id":"om_card"}
	}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	pc, ok := in.(core.PickCluster)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if pc.Reply.Cluster != "prod" || pc.Reply.MessageID != "om_card" {
		t.Fatalf("pickcluster = %+v", pc)
	}
}

func TestDecodeCardActionPickClusterEmpty(t *testing.T) {
	// Submit with nothing selected: no cluster in value or form_value.
	raw := []byte(`{
		"action": {"value": {"action":"pick_cluster"}, "form_value": {}},
		"context": {"open_chat_id":"oc_1","open_message_id":"om_card"}
	}`)
	in, err := decodeCardAction(raw)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	pc, ok := in.(core.PickCluster)
	if !ok {
		t.Fatalf("got %T", in)
	}
	if pc.Reply.Cluster != "" {
		t.Fatalf("want empty cluster, got %q", pc.Reply.Cluster)
	}
}
