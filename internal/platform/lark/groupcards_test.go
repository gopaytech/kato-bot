package lark

import (
	"strings"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestBuildGroupParentCardTallies(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
	s := core.GroupSummary{Group: g, Total: 10, Healthy: 6, Unhealthy: 2, Errored: 1, Unknown: 1}
	card := buildGroupParentCard(g, s, 10, true)
	for _, want := range []string{"critical", "prod-1", "6", "2", "10"} {
		if !strings.Contains(card, want) {
			t.Errorf("parent card missing %q: %s", want, card)
		}
	}
}

func TestBuildServiceReplyCardBuckets(t *testing.T) {
	g := core.Group{Name: "critical", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
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

func TestServiceReplyCardLabelsUseCase(t *testing.T) {
	g := core.Group{Name: "critical"}
	fls := false
	card := buildServiceReplyCard(g, core.ServiceResult{
		UseCase: "deployment-troubleshooting",
		Target:  map[string]string{"namespace": "payments", "deployment": "payment-api"},
		Healthy: &fls, Headline: "CrashLoopBackOff", Summary: "x",
	})
	if !strings.Contains(card, "deployment-troubleshooting") || !strings.Contains(card, "payments/payment-api") {
		t.Errorf("service card missing usecase·target label: %s", card)
	}
}

func TestParentCardShowsUseCaseCount(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
	}}
	card := buildGroupParentCard(g, core.GroupSummary{Group: g, Total: 2}, 0, false)
	if !strings.Contains(card, "2 targets") || !strings.Contains(card, "2 usecases") {
		t.Errorf("parent card should show targets + usecase count: %s", card)
	}
}

func TestConfirmCardHasSummaryButton(t *testing.T) {
	g := core.Group{Name: "critical", Cluster: "prod-1", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
	card := buildGroupConfirmCard(g)
	if !strings.Contains(card, "Run + summary") || !strings.Contains(card, "\"summary\":true") {
		t.Errorf("confirm card missing Run+summary button: %s", card)
	}
}

func TestBuildPickerCardHasGroupsSection(t *testing.T) {
	ucs := []core.UseCase{{Name: "dt", Description: "d", Ready: true}}
	groups := []core.Group{{Name: "critical", Items: []core.WorkItem{{UseCase: "dt", Inputs: map[string]string{"x": "y"}}}}}
	card := buildPickerCard("prod-1", ucs, groups)
	if !strings.Contains(card, "critical") || !strings.Contains(card, "pick_group") {
		t.Errorf("picker card missing groups section: %s", card)
	}
}
