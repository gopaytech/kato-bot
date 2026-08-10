package lark

import (
	"fmt"
	"strings"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// buildGroupParentCard renders the progress/rollup card. done is how many of
// total have completed; final flips the header from "running" to "done".
func buildGroupParentCard(g core.Group, s core.GroupSummary, done int, final bool) string {
	head := "⏳"
	state := "running"
	if final {
		head = "✅"
		state = "done"
	}
	elements := []any{
		markdown(fmt.Sprintf("%s **Group: %s** — %s", head, g.Name, state)),
		markdown(fmt.Sprintf("%s · %s · %d targets", g.UseCase, g.Cluster, s.Total)),
		markdown(fmt.Sprintf("Progress: **%d/%d**", done, s.Total)),
		markdown(fmt.Sprintf("🟢 %d   🔴 %d   ⚠️ %d   ❔ %d", s.Healthy, s.Unhealthy, s.Errored, s.Unknown)),
	}
	return card2(g.Name, elements)
}

// targetLabel renders a service target as a short display label: "namespace/deployment"
// when both are present, just "deployment" when only that's present, else "k=v k=v" for
// whatever keys the target carries.
func targetLabel(t map[string]string) string {
	ns, dep := t["namespace"], t["deployment"]
	switch {
	case ns != "" && dep != "":
		return ns + "/" + dep
	case dep != "":
		return dep
	default:
		var parts []string
		for k, v := range t {
			parts = append(parts, k+"="+v)
		}
		return strings.Join(parts, " ")
	}
}

// buildServiceReplyCard renders one service's outcome as a threaded reply.
func buildServiceReplyCard(g core.Group, r core.ServiceResult) string {
	label := targetLabel(r.Target)
	if r.Err != nil {
		return card2(g.Name, []any{
			markdown(fmt.Sprintf("⚠️ **%s** — check failed to run", label)),
			markdown(r.Err.Error()),
		})
	}
	icon := "❔"
	switch r.Bucket() {
	case "healthy":
		icon = "🟢"
	case "unhealthy":
		icon = "🔴"
	}
	head := fmt.Sprintf("%s **%s**", icon, label)
	if r.Headline != "" {
		head += " — " + r.Headline
	}
	elements := []any{markdown(head)}
	if r.Warning != "" {
		elements = append(elements, markdown("⚠️ "+r.Warning))
	}
	elements = append(elements,
		map[string]any{"tag": "hr"},
		markdown("📋 **Summary**\n"+r.Summary),
		markdown("_run: "+r.Run+"_"),
	)
	return card2(g.Name, elements)
}
