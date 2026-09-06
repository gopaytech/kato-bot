// Package lark is the Lark (Feishu) chat adapter for kato-bot.
package lark

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

// jsonStr marshals a card object to a compact JSON string. Card objects are built
// from map[string]any so the structure stays declarative and easy to assert in tests.
func jsonStr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Card objects here are always marshalable; a failure is a programming error.
		return fmt.Sprintf(`{"elements":[{"tag":"markdown","content":"card build error: %s"}]}`, err)
	}
	return string(b)
}

// card2 builds the Card JSON 2.0 envelope. The whole flow uses 2.0 so every Reply and
// Patch is the same schema (Lark rejects patching a card with a different-version card),
// and because the `form`/`input` components only exist in 2.0.
func card2(title string, elements []any) string {
	return jsonStr(map[string]any{
		"schema": "2.0",
		"header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": title}},
		"body":   map[string]any{"elements": elements},
	})
}

func markdown(content string) map[string]any {
	return map[string]any{"tag": "markdown", "content": content}
}

// button2 is a Card 2.0 callback button: the click delivers `value` as the callback's
// action.value (legacy used a top-level "value"; 2.0 uses a `behaviors` callback).
func button2(text string, value map[string]any) map[string]any {
	return map[string]any{
		"tag":       "button",
		"text":      map[string]any{"tag": "plain_text", "content": text},
		"type":      "primary",
		"behaviors": []any{map[string]any{"type": "callback", "value": value}},
	}
}

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

// buildPickerCard lists each UseCase with a Select button (ready ones) or a disabled note,
// followed by a Groups section (if any groups are configured for the cluster) with a
// Run group button per group.
func buildPickerCard(cluster string, ucs []core.UseCase, groups []core.Group) string {
	elements := []any{markdown("🔧 **kato** — pick a troubleshooting flow")}
	elements = append(elements, contextLines(cluster, nil)...)
	for _, uc := range ucs {
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, markdown(fmt.Sprintf("**%s**\n%s", uc.Name, uc.Description)))
		if uc.Ready {
			elements = append(elements, button2("Select ▸", map[string]any{"action": "pick", "cluster": cluster, "usecase": uc.Name}))
		} else {
			elements = append(elements, markdown("_not ready (failed validation in cluster)_"))
		}
	}
	if len(groups) > 0 {
		elements = append(elements, map[string]any{"tag": "hr"}, markdown("**Groups**"))
		for _, g := range groups {
			elements = append(elements, markdown(fmt.Sprintf("**%s** · %s · %d targets · %d usecases", g.Name, g.Cluster, len(g.WorkItems()), len(g.UseCaseCounts()))))
			elements = append(elements, button2("Run group ▸", map[string]any{"action": "pick_group", "cluster": cluster, "group": g.Name}))
		}
	}
	return card2("kato", elements)
}

// buildGroupConfirmCard asks the user to confirm running a group before it starts.
func buildGroupConfirmCard(g core.Group) string {
	elements := []any{
		markdown(fmt.Sprintf("📦 **Group: %s**", g.Name)),
		markdown(fmt.Sprintf("Run **%d** targets across **%d** usecases in **%s**?", len(g.WorkItems()), len(g.UseCaseCounts()), g.Cluster)),
	}
	for _, uc := range g.UseCaseCounts() {
		elements = append(elements, markdown(fmt.Sprintf("· %s (%d)", uc.UseCase, uc.Targets)))
	}
	elements = append(elements,
		button2("Run ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name}),
		button2("Run + summary ▸", map[string]any{"action": "run_group", "cluster": g.Cluster, "group": g.Name, "summary": true}),
	)
	return card2("kato", elements)
}

// buildGroupStartedCard acks that a group run has started; progress and results follow
// as separate cards/replies.
func buildGroupStartedCard(g core.Group) string {
	return card2("kato", []any{
		markdown(fmt.Sprintf("📦 **Group %s started** — running %d targets across %d usecases. Results will appear in this thread.",
			g.Name, len(g.WorkItems()), len(g.UseCaseCounts()))),
	})
}

// buildFormCard renders one input per declared input, prefilled, with an optional error
// banner and a Run submit button.
//
// This is a Card JSON 2.0 card ("schema":"2.0" + "body"): the `form` container and
// `input` components ONLY work in 2.0 — in the legacy format the inputs render but the
// submit button fires no callback. The submit button uses `form_action_type:"submit"`
// plus a `behaviors` callback (the legacy `action_type:"form_submit"` + `value` does
// nothing). The behavior's value lands in the callback's action.value and the input
// values in action.form_value, which decode.go reads.
func buildFormCard(cluster string, c core.Contract, prefill map[string]string, formErr string) string {
	runValue := map[string]any{"action": "run", "cluster": cluster, "usecase": c.Name}

	// No declared inputs: there's nothing to fill, so skip the form container entirely
	// and use a plain callback button — the exact button type that already works for the
	// picker's Select. (The form/form_submit path is only needed to collect input values.)
	if len(c.Inputs) == 0 {
		elems := []any{}
		if formErr != "" {
			elems = append(elems, markdown("⚠️ "+formErr))
		}
		elems = append(elems, markdown(fmt.Sprintf("🔧 **%s**\n%s", c.Name, c.Description)))
		elems = append(elems, contextLines(cluster, nil)...)
		elems = append(elems,
			markdown("_No inputs required — click Run._"),
			button2("▶ Run troubleshoot", runValue),
		)
		return card2(c.Name, elems)
	}

	formElems := []any{}
	if formErr != "" {
		formElems = append(formElems, markdown("⚠️ "+formErr))
	}
	formElems = append(formElems, markdown(fmt.Sprintf("🔧 **%s**\n%s", c.Name, c.Description)))
	formElems = append(formElems, contextLines(cluster, nil)...)
	for _, in := range c.Inputs {
		label := in.Name
		if in.Required {
			label += " *"
		}
		// Label as its own markdown line; the input carries only verified 2.0 attributes.
		formElems = append(formElems, markdown("**"+label+"**"))
		input := map[string]any{
			"tag":         "input",
			"name":        in.Name,
			"placeholder": map[string]any{"tag": "plain_text", "content": in.Name},
			"required":    in.Required,
		}
		if v := prefill[in.Name]; v != "" {
			input["default_value"] = v
		}
		formElems = append(formElems, input)
	}
	// Submit button, wrapped in a column_set to match Lark's documented form example.
	submitBtn := map[string]any{
		"tag":              "button",
		"text":             map[string]any{"tag": "plain_text", "content": "▶ Run troubleshoot"},
		"type":             "primary",
		"form_action_type": "submit",
		"name":             "submit",
		"behaviors":        []any{map[string]any{"type": "callback", "value": runValue}},
	}
	formElems = append(formElems, map[string]any{
		"tag": "column_set",
		"columns": []any{
			map[string]any{"tag": "column", "width": "auto", "elements": []any{submitBtn}},
		},
	})
	form := map[string]any{"tag": "form", "name": "kato_form", "elements": formElems}
	return card2(c.Name, []any{form})
}

// kvLine renders the inputs as "k=v  k=v" display text. Order is intentionally
// arbitrary (map iteration) — this is display-only and has no order-sensitive consumer.
func kvLine(inputs map[string]string) string {
	parts := make([]string, 0, len(inputs))
	for k, v := range inputs {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, "  ")
}

// contextLines renders the shared "which cluster / what inputs" block used by the picker,
// form, running, and result cards. The cluster line is always present; the inputs line is
// included only when inputs is non-empty (a nil or empty map renders no inputs line).
func contextLines(cluster string, inputs map[string]string) []any {
	lines := []any{markdown("☰ Cluster: " + cluster)}
	if len(inputs) > 0 {
		lines = append(lines, markdown("☰ Inputs: "+kvLine(inputs)))
	}
	return lines
}

// buildRunningCard is the immediate ack repaint shown while kato runs.
func buildRunningCard(cluster, useCase string, inputs map[string]string) string {
	elements := []any{markdown(fmt.Sprintf("⏳ **Running %s…**", useCase))}
	elements = append(elements, contextLines(cluster, inputs)...)
	elements = append(elements, markdown("_This can take up to ~30s while kato runs the checks and summarizes._"))
	return card2("kato", elements)
}

// buildResultCard renders the final summary (or a friendly error) plus a Run-again button.
// The cluster + inputs context block appears on both the success and error branches.
func buildResultCard(cluster, useCase string, inputs map[string]string, res core.RunResult) string {
	var elements []any
	if res.Err != nil {
		elements = []any{markdown(fmt.Sprintf("❌ **%s — could not run**", useCase))}
		elements = append(elements, contextLines(cluster, inputs)...)
		elements = append(elements, markdown(res.Err.Error()))
	} else {
		// Phase icon: kato reports "Completed" on success; anything else (e.g. "Failed") is not green.
		icon := "✅"
		if res.Phase != "Completed" {
			icon = "❌"
		}
		head := icon + " **" + useCase + " — " + res.Phase + "**"
		elements = []any{markdown(head)}
		elements = append(elements, contextLines(cluster, inputs)...)
		if res.Warning != "" {
			elements = append(elements, markdown("⚠️ "+res.Warning))
		}
		elements = append(elements,
			map[string]any{"tag": "hr"},
			markdown("📋 **Summary**\n"+res.Summary),
			map[string]any{"tag": "hr"},
			markdown("_run: "+res.Run+"_"),
		)
	}
	elements = append(elements, button2("↻ Run again", map[string]any{"action": "pick", "cluster": cluster, "usecase": useCase}))
	return card2(useCase, elements)
}

// buildErrorCard is a standalone error (e.g. kato unreachable at list time).
func buildErrorCard(msg string) string {
	return card2("kato", []any{markdown("❌ " + msg)})
}
