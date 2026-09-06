package lark

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
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
	// One select_static named "cluster", not one button per cluster.
	if !strings.Contains(card, `"tag":"select_static"`) {
		t.Error("expected a select_static dropdown")
	}
	if !strings.Contains(card, `"name":"cluster"`) {
		t.Error("select_static must be named cluster (its form_value key)")
	}
	// Labels/names appear as option text/value.
	if !strings.Contains(card, "Production") {
		t.Error("missing cluster label")
	}
	if !strings.Contains(card, `"value":"prod"`) || !strings.Contains(card, `"value":"staging"`) {
		t.Error("missing cluster option values (name fallback for staging)")
	}
	// Submit button carries ONLY the action; cluster is no longer in the value.
	if !strings.Contains(card, `"action":"pick_cluster"`) {
		t.Error("missing pick_cluster submit action")
	}
	if !strings.Contains(card, `"form_action_type":"submit"`) {
		t.Error("Select button must be a form submit")
	}
}

// clusterOptions digs body.elements[0](form).elements to find the select_static
// and returns its options, asserting the card structure along the way.
func clusterOptions(t *testing.T, card string) []any {
	t.Helper()
	m := asMap(t, card)
	body, ok := m["body"].(map[string]any)
	if !ok {
		t.Fatal("no body")
	}
	elems, ok := body["elements"].([]any)
	if !ok || len(elems) == 0 {
		t.Fatal("no body.elements")
	}
	form, ok := elems[0].(map[string]any)
	if !ok || form["tag"] != "form" {
		t.Fatalf("first element is not a form: %v", elems[0])
	}
	felems, ok := form["elements"].([]any)
	if !ok {
		t.Fatal("form has no elements")
	}
	for _, e := range felems {
		em, ok := e.(map[string]any)
		if ok && em["tag"] == "select_static" {
			opts, _ := em["options"].([]any)
			return opts
		}
	}
	t.Fatal("no select_static in form")
	return nil
}

func TestBuildClusterPickerCardOptions(t *testing.T) {
	opts := clusterOptions(t, buildClusterPickerCard([]core.Cluster{
		{Name: "prod", Label: "Production"},
		{Name: "staging"},
	}))
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d", len(opts))
	}
	first := opts[0].(map[string]any)
	if first["value"] != "prod" {
		t.Errorf("option[0].value = %v, want prod", first["value"])
	}
	text := first["text"].(map[string]any)
	if text["content"] != "Production" {
		t.Errorf("option[0].text.content = %v, want Production", text["content"])
	}
	second := opts[1].(map[string]any)
	secondText := second["text"].(map[string]any)
	if second["value"] != "staging" || secondText["content"] != "staging" {
		t.Errorf("option[1] = %v, want value+label both 'staging'", second)
	}
}

func TestBuildClusterPickerCardEmpty(t *testing.T) {
	card := buildClusterPickerCard(nil)
	m := asMap(t, card) // must be valid JSON, no panic
	if m["schema"] != "2.0" {
		t.Errorf("expected schema 2.0, got %v", m["schema"])
	}
	if opts := clusterOptions(t, card); len(opts) != 0 {
		t.Errorf("empty cluster list must yield 0 options, got %d", len(opts))
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
