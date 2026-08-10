package lark

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

func asMap(t *testing.T, jsonStr string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		t.Fatalf("card is not valid JSON: %v\n%s", err, jsonStr)
	}
	return m
}

func TestBuildClusterPickerCard(t *testing.T) {
	card := buildClusterPickerCard([]core.Cluster{
		{Name: "prod", Label: "Production"},
		{Name: "staging"},
	})
	m := asMap(t, card)
	if m["schema"] != "2.0" {
		t.Errorf("expected schema 2.0, got %v", m["schema"])
	}
	if !strings.Contains(card, "Production") {
		t.Error("missing cluster label")
	}
	if !strings.Contains(card, "staging") {
		t.Error("missing cluster name fallback")
	}
	if !strings.Contains(card, `"action":"pick_cluster"`) || !strings.Contains(card, `"cluster":"prod"`) {
		t.Error("missing pick_cluster action value")
	}
}

func TestBuildPickerCard(t *testing.T) {
	card := buildPickerCard("prod", []core.UseCase{
		{Name: "pod-crashloop", Description: "Diagnose crashloop", Ready: true},
		{Name: "broken", Description: "x", Ready: false},
	}, nil)
	m := asMap(t, card)
	if m["schema"] != "2.0" {
		t.Errorf("expected card schema 2.0, got %v", m["schema"])
	}
	body, ok := m["body"].(map[string]any)
	if !ok || body["elements"] == nil {
		t.Fatal("no body.elements")
	}
	if !strings.Contains(card, "pod-crashloop") {
		t.Error("missing usecase name")
	}
	if !strings.Contains(card, `"action":"pick"`) || !strings.Contains(card, `"usecase":"pod-crashloop"`) {
		t.Error("missing pick action value")
	}
	if !strings.Contains(card, `"cluster":"prod"`) {
		t.Error("pick action must carry the cluster")
	}
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("picker must show the cluster context line")
	}
}

func TestBuildFormCard(t *testing.T) {
	c := core.Contract{Name: "pod-crashloop", Description: "d", Inputs: []core.InputDecl{
		{Name: "namespace", Required: true}, {Name: "pod", Required: true},
	}}
	card := buildFormCard("prod", c, map[string]string{"namespace": "payments"}, "required: pod")
	if !strings.Contains(card, "namespace") || !strings.Contains(card, "pod") {
		t.Error("missing input names")
	}
	if !strings.Contains(card, "payments") {
		t.Error("missing prefill value")
	}
	if !strings.Contains(card, "required: pod") {
		t.Error("missing form error text")
	}
	if !strings.Contains(card, `"action":"run"`) || !strings.Contains(card, `"usecase":"pod-crashloop"`) {
		t.Error("missing run action value")
	}
	if !strings.Contains(card, `"cluster":"prod"`) {
		t.Error("run action must carry the cluster")
	}
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("form must show the cluster context line")
	}
	asMap(t, card)
}

func TestBuildFormCardNoInputs(t *testing.T) {
	// No declared inputs takes the non-form branch; it must still show the cluster line.
	c := core.Contract{Name: "node-health", Description: "d", Inputs: nil}
	card := buildFormCard("prod", c, nil, "")
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("no-input form must show the cluster context line")
	}
	if !strings.Contains(card, "No inputs required") {
		t.Error("no-input form must show the no-inputs note")
	}
	asMap(t, card)
}

func TestBuildFormCardNoInputsWithError(t *testing.T) {
	// No declared inputs + a form error: the error banner and the cluster line must
	// both render on the non-form branch.
	c := core.Contract{Name: "node-health", Description: "d", Inputs: nil}
	card := buildFormCard("prod", c, nil, "kato rejected the request")
	if !strings.Contains(card, "kato rejected the request") {
		t.Error("no-input form must show the form error banner")
	}
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("no-input form with error must still show the cluster context line")
	}
	asMap(t, card)
}

func TestBuildRunningCard(t *testing.T) {
	card := buildRunningCard("cluster-a", "pod-crashloop", map[string]string{"namespace": "payments"})
	if !strings.Contains(card, "Running") || !strings.Contains(card, "pod-crashloop") {
		t.Error("running card content")
	}
	if !strings.Contains(card, "Cluster: cluster-a") {
		t.Error("missing cluster line")
	}
	if !strings.Contains(card, "Inputs:") || !strings.Contains(card, "namespace=payments") {
		t.Error("missing inputs line")
	}
	asMap(t, card)
}

func TestBuildRunningCardNoInputs(t *testing.T) {
	card := buildRunningCard("cluster-a", "pod-crashloop", map[string]string{})
	if !strings.Contains(card, "Cluster: cluster-a") {
		t.Error("running card must show the cluster line even with no inputs")
	}
	if strings.Contains(card, "Inputs:") {
		t.Error("running card must not render an Inputs line when there are no inputs")
	}
	asMap(t, card)
}

func TestBuildResultCardCompleted(t *testing.T) {
	card := buildResultCard("prod", "pod-crashloop", map[string]string{"namespace": "payments"}, core.RunResult{
		Run: "pod-crashloop-abc", Phase: "Completed", Summary: "It is OOMKilled.",
	})
	if !strings.Contains(card, "It is OOMKilled.") || !strings.Contains(card, "pod-crashloop-abc") {
		t.Error("result card content")
	}
	if !strings.Contains(card, "✅") {
		t.Error("completed phase should show a green check")
	}
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("result card must show the cluster line")
	}
	if !strings.Contains(card, "namespace=payments") {
		t.Error("result card must show the inputs line")
	}
	if !strings.Contains(card, `"action":"pick"`) {
		t.Error("missing Run again action")
	}
	if !strings.Contains(card, `"cluster":"prod"`) {
		t.Error("run-again action must carry the cluster")
	}
	asMap(t, card)
}

func TestBuildResultCardFailedPhase(t *testing.T) {
	card := buildResultCard("prod", "pod-crashloop", map[string]string{"namespace": "payments"}, core.RunResult{
		Run: "pod-crashloop-abc", Phase: "Failed", Summary: "step errored",
	})
	if strings.Contains(card, "✅") {
		t.Error("failed phase must not show a green check")
	}
	if !strings.Contains(card, "❌") || !strings.Contains(card, "Failed") {
		t.Error("failed phase should show a red cross and the phase")
	}
	if !strings.Contains(card, "Cluster: prod") || !strings.Contains(card, "namespace=payments") {
		t.Error("failed-phase result must show cluster + inputs")
	}
	asMap(t, card)
}

func TestBuildResultCardError(t *testing.T) {
	card := buildResultCard("prod", "uc", map[string]string{"namespace": "payments"}, core.RunResult{Err: &core.RunError{Msg: "kato is busy"}})
	if !strings.Contains(card, "kato is busy") {
		t.Error("error not shown")
	}
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("error result must still show the cluster line")
	}
	if !strings.Contains(card, "namespace=payments") {
		t.Error("error result must still show the inputs line")
	}
	asMap(t, card)
}

func TestBuildResultCardNoInputs(t *testing.T) {
	card := buildResultCard("prod", "uc", map[string]string{}, core.RunResult{
		Run: "uc-1", Phase: "Completed", Summary: "ok",
	})
	if !strings.Contains(card, "Cluster: prod") {
		t.Error("result card must show the cluster line with no inputs")
	}
	if strings.Contains(card, "Inputs:") {
		t.Error("result card must not render an Inputs line when there are no inputs")
	}
	asMap(t, card)
}

func TestContextLines(t *testing.T) {
	// nil inputs: cluster line only, no Inputs line.
	only := contextLines("cluster-a", nil)
	if len(only) != 1 {
		t.Fatalf("nil inputs: want 1 element, got %d", len(only))
	}
	js := jsonStr(only)
	if !strings.Contains(js, "Cluster: cluster-a") {
		t.Errorf("missing cluster line: %s", js)
	}
	if strings.Contains(js, "Inputs:") {
		t.Errorf("nil inputs must not render an Inputs line: %s", js)
	}

	// empty map behaves like nil.
	if len(contextLines("c", map[string]string{})) != 1 {
		t.Error("empty inputs map must not render an Inputs line")
	}

	// non-empty inputs: cluster line + inputs line.
	both := contextLines("cluster-a", map[string]string{"namespace": "prod"})
	if len(both) != 2 {
		t.Fatalf("with inputs: want 2 elements, got %d", len(both))
	}
	js2 := jsonStr(both)
	if !strings.Contains(js2, "Cluster: cluster-a") || !strings.Contains(js2, "namespace=prod") {
		t.Errorf("with inputs must render cluster + inputs: %s", js2)
	}
}
