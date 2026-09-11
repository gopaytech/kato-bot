// Package summaryfmt converts a kato run summary — which kato emits either as
// Markdown or as a structured JSON block document (kato's summaryFormat field) —
// into the format each chat platform needs: Lark consumes Markdown natively,
// Telegram needs a restricted HTML subset. Both input formats are normalized to
// Markdown first, then Telegram HTML is produced from that single path.
package summaryfmt

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// --- kato JSON summary schema (mirror of kato's json-mode document) ---

type block struct {
	Type     string   `json:"type"`
	Text     string   `json:"text"`
	Items    []string `json:"items"`
	Language string   `json:"language"`
}

type doc struct {
	Verdict  string  `json:"verdict"`
	Headline string  `json:"headline"`
	Blocks   []block `json:"blocks"`
}

func isJSON(format string) bool { return strings.EqualFold(strings.TrimSpace(format), "json") }

// ToMarkdown returns canonical Markdown for a kato summary regardless of the
// declared format. For "json" it renders the block document to Markdown; for
// markdown/empty/unknown it returns summary unchanged. If json parsing fails it
// falls back to the raw text (kato itself downgrades invalid json to markdown, so
// this is only a defensive net).
func ToMarkdown(summary, format string) string {
	if !isJSON(format) {
		return summary
	}
	d, ok := parseDoc(summary)
	if !ok {
		return summary
	}
	return blocksToMarkdown(d.Blocks)
}

// ToTelegramHTML returns Telegram-safe HTML (only <b>/<i>/<code>/<pre>/<a> tags)
// for a kato summary in either format.
func ToTelegramHTML(summary, format string) string {
	return markdownToTelegramHTML(ToMarkdown(summary, format))
}

func parseDoc(s string) (doc, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return doc{}, false
	}
	var d doc
	if err := json.Unmarshal([]byte(s), &d); err != nil || len(d.Blocks) == 0 {
		return doc{}, false
	}
	return d, true
}

func blocksToMarkdown(blocks []block) string {
	var b strings.Builder
	for _, bl := range blocks {
		switch strings.ToLower(strings.TrimSpace(bl.Type)) {
		case "heading":
			// Rendered as bold rather than a "#" heading so it is bold on both
			// Lark and Telegram (Telegram has no heading tag).
			if t := strings.TrimSpace(bl.Text); t != "" {
				b.WriteString("**" + t + "**\n\n")
			}
		case "paragraph":
			if t := strings.TrimSpace(bl.Text); t != "" {
				b.WriteString(t + "\n\n")
			}
		case "list":
			for _, it := range bl.Items {
				if it = strings.TrimSpace(it); it != "" {
					b.WriteString("- " + it + "\n")
				}
			}
			b.WriteString("\n")
		case "code":
			b.WriteString("```" + strings.TrimSpace(bl.Language) + "\n" + bl.Text + "\n```\n\n")
		default:
			if t := strings.TrimSpace(bl.Text); t != "" {
				b.WriteString(t + "\n\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- Markdown (constrained GFM subset) -> Telegram HTML ---

var (
	reHeading   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	reBullet    = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	reOrdered   = regexp.MustCompile(`^\s*(\d+)\.\s+(.*)$`)
	reFence     = regexp.MustCompile("^\\s*```")
	reBlankRuns = regexp.MustCompile(`\n{3,}`)

	reCodeSpan = regexp.MustCompile("`([^`]+)`")
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reBold     = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	reItalic   = regexp.MustCompile(`\*([^*\n]+)\*|_([^_\n]+)_`)
)

func markdownToTelegramHTML(md string) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if reFence.MatchString(line) { // fenced code block: emit verbatim in <pre>
			var code []string
			i++
			for i < len(lines) && !reFence.MatchString(lines[i]) {
				code = append(code, lines[i])
				i++
			}
			out = append(out, "<pre>"+html.EscapeString(strings.Join(code, "\n"))+"</pre>")
			continue
		}
		if m := reHeading.FindStringSubmatch(line); m != nil {
			out = append(out, "<b>"+inline(m[1])+"</b>")
			continue
		}
		if m := reBullet.FindStringSubmatch(line); m != nil {
			out = append(out, "• "+inline(m[1]))
			continue
		}
		if m := reOrdered.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+". "+inline(m[2]))
			continue
		}
		out = append(out, inline(line))
	}
	res := reBlankRuns.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
	return strings.TrimSpace(res)
}

func codePlaceholder(i int) string { return "\x00c" + strconv.Itoa(i) + "\x00" }
func linkPlaceholder(i int) string { return "\x00l" + strconv.Itoa(i) + "\x00" }

// inline converts inline Markdown (code spans, links, bold, italic) to Telegram
// HTML on a single line. Code spans and link targets are extracted first so bold
// and italic never bleed into them, then the remaining text is HTML-escaped, then
// emphasis is applied, then the protected fragments are restored (already escaped).
func inline(s string) string {
	var codes, links []string

	s = reCodeSpan.ReplaceAllStringFunc(s, func(m string) string {
		sub := reCodeSpan.FindStringSubmatch(m)
		codes = append(codes, "<code>"+html.EscapeString(sub[1])+"</code>")
		return codePlaceholder(len(codes) - 1)
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		links = append(links, `<a href="`+html.EscapeString(sub[2])+`">`+html.EscapeString(sub[1])+`</a>`)
		return linkPlaceholder(len(links) - 1)
	})

	s = html.EscapeString(s)

	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		sub := reBold.FindStringSubmatch(m)
		inner := sub[1]
		if inner == "" {
			inner = sub[2]
		}
		return "<b>" + inner + "</b>"
	})
	s = reItalic.ReplaceAllStringFunc(s, func(m string) string {
		sub := reItalic.FindStringSubmatch(m)
		inner := sub[1]
		if inner == "" {
			inner = sub[2]
		}
		return "<i>" + inner + "</i>"
	})

	for i, c := range codes {
		s = strings.ReplaceAll(s, codePlaceholder(i), c)
	}
	for i, l := range links {
		s = strings.ReplaceAll(s, linkPlaceholder(i), l)
	}
	return s
}
