# Cluster Picker `select_static` Dropdown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render the Lark cluster picker as one `select_static` dropdown (with a submit button) instead of one callback button per cluster, so the card renders regardless of cluster count.

**Architecture:** The cluster picker card becomes a Card JSON 2.0 `form` containing a single `select_static` (options = every cluster) plus a submit button carrying `{action: "pick_cluster"}`. The chosen cluster now arrives in the callback's `form_value["cluster"]` (keyed by the select's `name`) instead of the button's `value`. `decodeCardAction` reads it from `form_value`, falling back to the existing `value["cluster"]` so stale cards still work. No `core/*`, `render.go`, `cardaction.go`, REST, MCP, or group-picker changes.

**Tech Stack:** Go, `github.com/larksuite/oapi-sdk-go/v3`, standard `testing`. Cards are built from `map[string]any` and marshaled with `encoding/json`.

## Global Constraints

- **DO NOT COMMIT.** Another agent shares this working tree. Implementers skip every `git commit` step; stage nothing on their behalf. The coordinator snapshots via `git commit-tree` for review packages. Wherever a step below would say "commit", it instead says "stop — do not commit".
- All cards use Card JSON 2.0 (`"schema": "2.0"`) — the `form`/`select_static`/`input` components only work in 2.0.
- Package under change: `github.com/zufardhiyaulhaq/kato-bot/internal/platform/lark`.
- Run tests with the race detector: `go test -race ./internal/platform/lark/`.
- Do not change the `core.Renderer` port, `buildClusterPickerCard`'s Go signature (`[]core.Cluster` in), REST/MCP, or the group/use-case picker (`buildPickerCard`).

---

### Task 1: Cluster picker renders as a `select_static` form

**Files:**
- Modify: `internal/platform/lark/cards.go` (rewrite `buildClusterPickerCard`, lines ~49-64)
- Test: `internal/platform/lark/cards_test.go` (rewrite `TestBuildClusterPickerCard`, lines ~20-38; add two tests)

**Interfaces:**
- Consumes: `core.Cluster{Name, Label string}` (unchanged); existing helpers in `cards.go`: `card2(title string, elements []any) string`, `markdown(content string) map[string]any`, `jsonStr`.
- Produces: `buildClusterPickerCard(clusters []core.Cluster) string` — same signature. The returned card is a 2.0 `form` (name `kato_cluster_form`) whose elements are: a header `markdown`, one `select_static` (`name:"cluster"`, one option per cluster `{text:{tag:"plain_text",content:<label>}, value:<name>}`), and a submit button (wrapped in a `column_set`) whose `behaviors[0].value == {"action":"pick_cluster"}`. `<label>` = `cl.Label` or `cl.Name` when Label is empty; option `value` is always `cl.Name`.

- [ ] **Step 1: Rewrite the existing card test to expect the dropdown**

Replace the whole `TestBuildClusterPickerCard` in `internal/platform/lark/cards_test.go` with:

```go
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
```

- [ ] **Step 2: Add a structural test — one option per cluster, in order**

Append to `internal/platform/lark/cards_test.go`:

```go
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
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test -race ./internal/platform/lark/ -run 'TestBuildClusterPickerCard' -v`
Expected: FAIL — the current `buildClusterPickerCard` emits per-cluster buttons, so `"tag":"select_static"` / the `form` structure assertions fail.

- [ ] **Step 4: Rewrite `buildClusterPickerCard`**

In `internal/platform/lark/cards.go`, replace the existing `buildClusterPickerCard` (and its doc comment) with:

```go
// buildClusterPickerCard renders every configured cluster as one option in a single
// select_static dropdown inside a 2.0 form, plus a Select submit button. This keeps the
// card to two components regardless of cluster count (one interactive button per cluster
// overflows Lark's card size once there are a few dozen clusters). The Lark client filters
// the option list as the user types (built-in typeahead). The chosen cluster is delivered
// in the callback's form_value under the select's name ("cluster"), not the button value.
func buildClusterPickerCard(clusters []core.Cluster) string {
	options := make([]any, 0, len(clusters))
	for _, cl := range clusters {
		label := cl.Label
		if label == "" {
			label = cl.Name
		}
		options = append(options, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": label},
			"value": cl.Name,
		})
	}
	sel := map[string]any{
		"tag":         "select_static",
		"name":        "cluster",
		"placeholder": map[string]any{"tag": "plain_text", "content": "Select a cluster…"},
		"options":     options,
	}
	// Submit button, wrapped in a column_set to match Lark's documented form example
	// (the same wrapping buildFormCard uses so the submit reliably fires its callback).
	submitBtn := map[string]any{
		"tag":              "button",
		"text":             map[string]any{"tag": "plain_text", "content": "Select ▸"},
		"type":             "primary",
		"form_action_type": "submit",
		"name":             "submit",
		"behaviors":        []any{map[string]any{"type": "callback", "value": map[string]any{"action": "pick_cluster"}}},
	}
	submitCol := map[string]any{
		"tag": "column_set",
		"columns": []any{
			map[string]any{"tag": "column", "width": "auto", "elements": []any{submitBtn}},
		},
	}
	form := map[string]any{
		"tag":  "form",
		"name": "kato_cluster_form",
		"elements": []any{
			markdown("☸️ **kato** — pick a cluster"),
			sel,
			submitCol,
		},
	}
	return card2("kato", []any{form})
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/platform/lark/ -run 'TestBuildClusterPickerCard' -v`
Expected: PASS (all three: `TestBuildClusterPickerCard`, `TestBuildClusterPickerCardOptions`, `TestBuildClusterPickerCardEmpty`).

- [ ] **Step 6: Run the whole lark package to confirm no regressions**

Run: `go test -race ./internal/platform/lark/`
Expected: PASS (no other test asserted the old per-button cluster shape).

- [ ] **Step 7: Stop — do NOT commit**

Per Global Constraints, leave changes unstaged/uncommitted. Report the diff for review.

---

### Task 2: Decode `pick_cluster` from `form_value`

**Files:**
- Modify: `internal/platform/lark/decode.go` (the `case "pick_cluster":` branch, lines ~44-45)
- Test: `internal/platform/lark/decode_test.go` (add two tests)

**Interfaces:**
- Consumes: `cardActionPayload` (already defined in `decode.go`) — has `Action.Value map[string]any` and `Action.FormValue map[string]string`. `core.PickCluster{Reply core.Reply}` with `Reply.Cluster string`.
- Produces: `decodeCardAction([]byte) (core.Intent, error)` — for `pick_cluster`, `Reply.Cluster` is taken from `form_value["cluster"]` when non-empty, otherwise from the existing top-level `value["cluster"]` read (backward compatibility with stale cards). Empty in both → empty `Reply.Cluster` (drives the core's existing "no cluster selected — start over").

- [ ] **Step 1: Write the failing tests**

Append to `internal/platform/lark/decode_test.go`:

```go
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
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test -race ./internal/platform/lark/ -run 'TestDecodeCardActionPickCluster' -v`
Expected: `TestDecodeCardActionPickClusterFromForm` FAILS — the current code reads cluster only from `value`, so `Reply.Cluster` is empty. (`TestDecodeCardActionPickClusterEmpty` may already pass; that is fine.)

- [ ] **Step 3: Read `form_value["cluster"]` in the `pick_cluster` branch**

In `internal/platform/lark/decode.go`, replace:

```go
	case "pick_cluster":
		return core.PickCluster{Reply: reply}, nil
```

with:

```go
	case "pick_cluster":
		// The select_static delivers its choice in form_value (keyed by the select's
		// name). Prefer it; fall back to value["cluster"] for older/stale cards that
		// still carry the cluster in the button value.
		if fv := p.Action.FormValue["cluster"]; fv != "" {
			reply.Cluster = fv
		}
		return core.PickCluster{Reply: reply}, nil
```

(The top-level `cluster, _ := p.Action.Value["cluster"].(string)` and `reply := core.Reply{... Cluster: cluster}` lines above stay as-is — they provide the fallback.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/platform/lark/ -run 'TestDecodeCardActionPickCluster' -v`
Expected: PASS for all three — `TestDecodeCardActionPickCluster` (existing, value path), `TestDecodeCardActionPickClusterFromForm` (form path), `TestDecodeCardActionPickClusterEmpty`.

- [ ] **Step 5: Run the whole lark package**

Run: `go test -race ./internal/platform/lark/`
Expected: PASS.

- [ ] **Step 6: Build the module**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 7: Stop — do NOT commit**

Per Global Constraints, leave changes unstaged/uncommitted. Report the diff for review.

---

### Task 3: Manual Lark verification (human-in-the-loop)

**Why:** Unit tests assert only the JSON we build — they cannot prove Lark renders the `select_static` or that the pick lands in `form_value["cluster"]`. This task is a checklist for the coordinator/user to run once against a real Lark tenant; there is no code to write unless it fails.

**Files:** none (verification only).

- [ ] **Step 1: Render the picker**

`@`-mention the bot in Lark. Expected: a "kato — pick a cluster" card with a single **"Select a cluster…"** dropdown (not a list of buttons), listing every configured cluster.

- [ ] **Step 2: Confirm typeahead**

Open the dropdown and type part of a cluster name. Expected: the option list filters client-side to matches (as in the reference "Create a ticket" card).

- [ ] **Step 3: Confirm the pick advances**

Select a cluster and click **Select ▸**. Expected: the card advances to the use-case / group picker **for that cluster** (proves `form_value["cluster"]` reached the decode and resolved in the registry).

- [ ] **Step 4: If the pick does NOT advance (cluster came back empty)**

The selection is arriving in `action.option` (direct-callback form), not `form_value`. Fix: in the `pick_cluster` branch of `decode.go`, also read `option`:

```go
	case "pick_cluster":
		if fv := p.Action.FormValue["cluster"]; fv != "" {
			reply.Cluster = fv
		} else if opt, _ := p.Action.Value["option"].(string); opt != "" {
			reply.Cluster = opt
		}
		return core.PickCluster{Reply: reply}, nil
```

Add `Option` decoding to `cardActionPayload.Action` if `option` is not inside `value` (check the real payload logged by the bot). Re-run Task 2's tests plus this checklist.

- [ ] **Step 5: Report the result**

Report whether the picker rendered, filtered, and advanced. Do not commit.

---

## Self-Review

**Spec coverage:**
- Spec "Component behavior (Lark)" — Task 1 (select_static form, options, submit button carrying only `pick_cluster`). ✓
- Spec "Decode change" (form_value with value fallback; empty → start over) — Task 2. ✓
- Spec "Testing" unit cases (schema, one option per cluster in order, empty list, decode from form_value, decode empty) — Task 1 Steps 1-2 + Task 2 Step 1. ✓
- Spec "Testing" manual verification (renders, filters, advances, form_value-vs-option fallback) — Task 3. ✓
- Spec "What does NOT change" — Global Constraints forbid touching core/render/cardaction/REST/MCP/group picker; no task edits them. ✓

**Placeholder scan:** No TBD/TODO; every code step has concrete code. Task 3 is intentionally verification-only (no code) and says so. ✓

**Type consistency:** `buildClusterPickerCard(clusters []core.Cluster) string` unchanged across spec/plan/tests. Select `name` is `"cluster"` in the card (Task 1) and read as `form_value["cluster"]` in decode (Task 2) and tests — consistent. Submit action string `"pick_cluster"` matches the existing `case "pick_cluster":`. `core.PickCluster{Reply}` / `Reply.Cluster` match `types.go`. ✓
