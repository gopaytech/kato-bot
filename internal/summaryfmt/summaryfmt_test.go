package summaryfmt

import "strings"

import "testing"

func TestToMarkdown_MarkdownModeUnchanged(t *testing.T) {
	in := "## Diagnosis\n\nThe **web** deployment is down.\n- a\n- b"
	for _, f := range []string{"", "markdown", "MARKDOWN", "weird"} {
		if got := ToMarkdown(in, f); got != in {
			t.Errorf("format %q: ToMarkdown mutated markdown input:\n%q", f, got)
		}
	}
}

func TestToMarkdown_JSONBlocks(t *testing.T) {
	js := `{"verdict":"unhealthy","headline":"bad","blocks":[
		{"type":"heading","text":"Diagnosis"},
		{"type":"paragraph","text":"web has 0/3 ready"},
		{"type":"list","items":["image :v2 not found","3 pods CrashLoopBackOff"]},
		{"type":"code","text":"kubectl get pods","language":"shell"}]}`
	got := ToMarkdown(js, "json")
	for _, want := range []string{"**Diagnosis**", "web has 0/3 ready", "- image :v2 not found", "- 3 pods CrashLoopBackOff", "```shell", "kubectl get pods"} {
		if !strings.Contains(got, want) {
			t.Errorf("json->markdown missing %q in:\n%s", want, got)
		}
	}
}

func TestToMarkdown_JSONInvalidFallsBackToRaw(t *testing.T) {
	for _, raw := range []string{"not json", `{"verdict":"healthy"}`, `{"blocks":[]}`} {
		if got := ToMarkdown(raw, "json"); got != raw {
			t.Errorf("invalid json should fall back to raw; got %q want %q", got, raw)
		}
	}
}

func TestToTelegramHTML_Markdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"## Diagnosis", "<b>Diagnosis</b>"},
		{"plain **bold** text", "plain <b>bold</b> text"},
		{"an _italic_ word", "an <i>italic</i> word"},
		{"use `kubectl get`", "use <code>kubectl get</code>"},
		{"- first\n- second", "• first\n• second"},
		{"see [docs](https://x.io)", `see <a href="https://x.io">docs</a>`},
	}
	for _, c := range cases {
		if got := ToTelegramHTML(c.in, "markdown"); got != c.want {
			t.Errorf("ToTelegramHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToTelegramHTML_EscapesHTMLSpecials(t *testing.T) {
	got := ToTelegramHTML("compare a < b && c > d", "markdown")
	if strings.ContainsAny(got, "<>") && !strings.Contains(got, "&lt;") {
		t.Errorf("raw < not escaped: %q", got)
	}
	for _, want := range []string{"&lt;", "&gt;", "&amp;"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing entity %q in %q", want, got)
		}
	}
}

func TestToTelegramHTML_FencedCodeVerbatimAndEscaped(t *testing.T) {
	in := "before\n```go\nif a < b && c { }\n```\nafter"
	got := ToTelegramHTML(in, "markdown")
	if !strings.Contains(got, "<pre>") || !strings.Contains(got, "</pre>") {
		t.Errorf("expected <pre> block, got %q", got)
	}
	if !strings.Contains(got, "if a &lt; b &amp;&amp; c { }") {
		t.Errorf("code should be escaped and verbatim, got %q", got)
	}
	// bold/italic markers inside code must NOT be interpreted
	if strings.Contains(got, "<b>") {
		t.Errorf("no emphasis expected in this input, got %q", got)
	}
}

func TestToTelegramHTML_NoEmphasisInsideCodeSpan(t *testing.T) {
	got := ToTelegramHTML("run `a_b_c` now", "markdown")
	want := "run <code>a_b_c</code> now"
	if got != want {
		t.Errorf("code span should suppress italic: got %q want %q", got, want)
	}
}

func TestToTelegramHTML_JSON(t *testing.T) {
	js := `{"verdict":"unhealthy","headline":"bad","blocks":[
		{"type":"heading","text":"Diagnosis"},
		{"type":"list","items":["image not found"]}]}`
	got := ToTelegramHTML(js, "json")
	for _, want := range []string{"<b>Diagnosis</b>", "• image not found"} {
		if !strings.Contains(got, want) {
			t.Errorf("json->telegram missing %q in:\n%s", want, got)
		}
	}
}
