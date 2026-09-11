package summaryfmt

import (
	"strings"
	"testing"
)

// These tests pin the backward-compatibility contract: an un-upgraded kato does
// not send summaryFormat, so kato-bot receives format == "". That MUST be treated
// as Markdown (kato's historical output), never as JSON.

func TestCompat_EmptyFormatRendersLikeMarkdown(t *testing.T) {
	md := "## Diagnosis\nThe **web** pod is <down>.\n- a\n- b\nrun `kubectl get po` & wait"

	// Lark: passed through unchanged (its native markdown card, exactly as before).
	if got := ToMarkdown(md, ""); got != md {
		t.Errorf("empty format must pass markdown through unchanged, got:\n%s", got)
	}

	// Telegram: empty format renders identically to explicit "markdown".
	if ToTelegramHTML(md, "") != ToTelegramHTML(md, "markdown") {
		t.Error("empty format (old kato) must render like markdown on Telegram")
	}

	// And it produces real HTML with escaping — not raw markdown, not broken tags.
	got := ToTelegramHTML(md, "")
	for _, want := range []string{"<b>Diagnosis</b>", "<b>web</b>", "• a", "&lt;down&gt;", "<code>kubectl get po</code>", "&amp;"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty-format markdown not converted/escaped as expected: missing %q in:\n%s", want, got)
		}
	}
}

func TestCompat_JSONLikeContentNotParsedWhenFormatEmpty(t *testing.T) {
	// A summary that happens to look like JSON must NOT be parsed as JSON when the
	// format marker is absent — parsing is gated on summaryFormat, never sniffed.
	s := `{"verdict":"healthy","blocks":[{"type":"paragraph","text":"x"}]}`
	if got := ToMarkdown(s, ""); got != s {
		t.Errorf("json-looking content with empty format must be treated as text, got:\n%s", got)
	}
}
