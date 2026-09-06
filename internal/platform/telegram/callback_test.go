package telegram

import (
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestCallbackRoundTrip(t *testing.T) {
	cases := []struct {
		data string
		want core.Intent
	}{
		{cbCluster("prod"), core.PickCluster{Reply: core.Reply{Cluster: "prod"}}},
		{cbUseCase("prod", "deploy-check"), core.PickUseCase{Reply: core.Reply{Cluster: "prod"}, Name: "deploy-check"}},
		{cbRun("prod", "deploy-check"), core.SubmitForm{Reply: core.Reply{Cluster: "prod"}, Name: "deploy-check", Inputs: map[string]string{}}},
		{cbGroup("prod", "critical"), core.PickGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical"}},
		{cbRunGroup("prod", "critical", true), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical", Summary: true}},
		{cbRunGroup("prod", "critical", false), core.RunGroup{Reply: core.Reply{Cluster: "prod"}, Name: "critical", Summary: false}},
	}
	for _, c := range cases {
		got, err := decodeCB(c.data)
		if err != nil {
			t.Fatalf("decode %q: %v", c.data, err)
		}
		if !intentEqual(got, c.want) {
			t.Fatalf("decode %q = %#v, want %#v", c.data, got, c.want)
		}
	}
}

func TestCallbackUnknownAction(t *testing.T) {
	if _, err := decodeCB("z|prod|x"); err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestCallbackFits(t *testing.T) {
	if !cbFits(cbUseCase("prod", "deployment-troubleshooting")) {
		t.Fatal("realistic payload should fit 64 bytes")
	}
}

// intentEqual compares the intent variants this package produces (ignores Reply
// addressing beyond Cluster, which is all decodeCB sets).
func intentEqual(a, b core.Intent) bool {
	switch av := a.(type) {
	case core.PickCluster:
		bv, ok := b.(core.PickCluster)
		return ok && av.Reply.Cluster == bv.Reply.Cluster
	case core.PickUseCase:
		bv, ok := b.(core.PickUseCase)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name
	case core.SubmitForm:
		bv, ok := b.(core.SubmitForm)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name && len(av.Inputs) == len(bv.Inputs)
	case core.PickGroup:
		bv, ok := b.(core.PickGroup)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name
	case core.RunGroup:
		bv, ok := b.(core.RunGroup)
		return ok && av.Reply.Cluster == bv.Reply.Cluster && av.Name == bv.Name && av.Summary == bv.Summary
	}
	return false
}
