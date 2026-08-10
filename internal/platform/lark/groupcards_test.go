package lark

import (
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

func TestBuildGroupParentCardTallies(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", UseCase: "dt"}
	s := core.GroupSummary{Group: g, Total: 10, Healthy: 6, Unhealthy: 2, Errored: 1, Unknown: 1}
	card := buildGroupParentCard(g, s, 10, true)
	for _, want := range []string{"critical", "prod-1", "6", "2", "10"} {
		if !strings.Contains(card, want) {
			t.Errorf("parent card missing %q: %s", want, card)
		}
	}
}

func TestBuildServiceReplyCardBuckets(t *testing.T) {
	g := core.Group{Name: "critical", UseCase: "dt"}
	fls := false
	unhealthy := buildServiceReplyCard(g, core.ServiceResult{
		Target:  map[string]string{"namespace": "payments", "deployment": "payment-api"},
		Healthy: &fls, Headline: "CrashLoopBackOff", Summary: "pods crashing",
	})
	if !strings.Contains(unhealthy, "payment-api") || !strings.Contains(unhealthy, "CrashLoopBackOff") {
		t.Errorf("unhealthy reply missing target/headline: %s", unhealthy)
	}
	errored := buildServiceReplyCard(g, core.ServiceResult{
		Target: map[string]string{"deployment": "auth-api"}, Err: &core.RunError{Msg: "kato busy"},
	})
	if !strings.Contains(errored, "auth-api") || !strings.Contains(errored, "kato busy") {
		t.Errorf("errored reply missing target/error: %s", errored)
	}
}

func TestBuildPickerCardHasGroupsSection(t *testing.T) {
	ucs := []core.UseCase{{Name: "dt", Description: "d", Ready: true}}
	groups := []core.Group{{Name: "critical", UseCase: "dt", Targets: []map[string]string{{"x": "y"}}}}
	card := buildPickerCard("prod-1", ucs, groups)
	if !strings.Contains(card, "critical") || !strings.Contains(card, "pick_group") {
		t.Errorf("picker card missing groups section: %s", card)
	}
}
