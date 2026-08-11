package summary

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// DefaultMaxEvidenceBytes bounds the evidence sent to the LLM.
const DefaultMaxEvidenceBytes = 16384

// perItemSummaryCap bounds a non-healthy service's summary in the evidence, in runes.
const perItemSummaryCap = 400

const systemPrompt = `You are a Kubernetes SRE. You are given the per-service results of a batch of troubleshooting checks run across a group. Write a concise group health summary: the overall status (how many healthy vs not), the notable failures and any common theme, and the single most useful next action. Use ONLY the evidence provided; do not invent data.`

// truncateHeadTail shortens s to at most max runes as head…tail (rune-safe).
func truncateHeadTail(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 4 {
		return string(r[:max])
	}
	head := max / 2
	tail := max - head - 1 // room for the ellipsis rune
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// BuildEvidence renders the group's results as a bounded evidence block: one line
// per service (usecase · target · bucket · headline), plus a separately bounded,
// head+tail-truncated per-service summary for non-healthy services, capped to
// maxBytes. An over-length item is skipped (not fatal) so later, shorter
// services still get their evidence.
func BuildEvidence(results []core.ServiceResult, maxBytes int) string {
	var b strings.Builder
	truncated := false
	for _, r := range results {
		header := fmt.Sprintf("- %s · %s · %s", r.UseCase, targetLabel(r.Target), r.Bucket())
		if r.Headline != "" {
			header += " — " + r.Headline
		}
		header += "\n"
		if b.Len()+len(header) > maxBytes {
			truncated = true
			continue // skip this one; a later (shorter) service line may still fit
		}
		b.WriteString(header)
		if r.Bucket() != "healthy" {
			if s := strings.TrimSpace(r.Summary); s != "" {
				detail := "    " + oneLine(truncateHeadTail(s, perItemSummaryCap)) + "\n"
				if b.Len()+len(detail) <= maxBytes {
					b.WriteString(detail)
				} else {
					truncated = true
				}
			}
		}
	}
	if truncated {
		b.WriteString("[... evidence truncated to fit budget ...]\n")
	}
	return b.String()
}

// Summarize builds the prompt and calls the client. It is total: a nil client
// (unconfigured) or a client error yields an empty summary and a non-empty
// warning — never an error, never a panic.
func Summarize(ctx context.Context, client Client, g core.Group, results []core.ServiceResult, maxEvidenceBytes int) (summary string, warning string) {
	if client == nil {
		return "", "group summary not configured"
	}
	if maxEvidenceBytes <= 0 {
		maxEvidenceBytes = DefaultMaxEvidenceBytes
	}
	user := fmt.Sprintf("Group: %s (cluster %s)\n\nResults:\n%s", g.Name, g.Cluster, BuildEvidence(results, maxEvidenceBytes))
	out, err := client.Complete(ctx, systemPrompt, user)
	if err != nil {
		return "", "group summary unavailable: " + err.Error()
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "", "group summary empty (model returned no content)"
	}
	return trimmed, ""
}

func targetLabel(t map[string]string) string {
	ns, dep := t["namespace"], t["deployment"]
	switch {
	case ns != "" && dep != "":
		return ns + "/" + dep
	case dep != "":
		return dep
	default:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, k+"="+t[k])
		}
		return strings.Join(parts, " ")
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
