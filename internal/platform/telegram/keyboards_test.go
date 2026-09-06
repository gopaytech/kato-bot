package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gopaytech/kato-bot/internal/core"
)

func TestClusterPickerKB(t *testing.T) {
	_, kb := clusterPickerKB([]core.Cluster{{Name: "prod", Label: "Production"}, {Name: "dev"}})
	if kb == nil || len(kb.InlineKeyboard) != 2 {
		t.Fatalf("want 2 rows, got %#v", kb)
	}
	b0 := kb.InlineKeyboard[0][0]
	if b0.Text != "Production" || b0.CallbackData != "c|prod" {
		t.Fatalf("row0 wrong: text=%q data=%q", b0.Text, b0.CallbackData)
	}
	// Label defaults to Name when empty.
	if kb.InlineKeyboard[1][0].Text != "dev" {
		t.Fatalf("row1 label default wrong: %q", kb.InlineKeyboard[1][0].Text)
	}
}

func TestPickerKBSplitsUsecasesAndGroups(t *testing.T) {
	_, kb := pickerKB("prod",
		[]core.UseCase{{Name: "deploy-check"}},
		[]core.Group{{Name: "critical"}},
	)
	var datas []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			datas = append(datas, b.CallbackData)
		}
	}
	joined := strings.Join(datas, ",")
	if !strings.Contains(joined, "u|prod|deploy-check") || !strings.Contains(joined, "g|prod|critical") {
		t.Fatalf("missing usecase/group buttons: %s", joined)
	}
}

func TestGroupConfirmKBHasBothRunButtons(t *testing.T) {
	_, kb := groupConfirmKB(core.Group{Name: "critical", Cluster: "prod"})
	var datas []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			datas = append(datas, b.CallbackData)
		}
	}
	j := strings.Join(datas, ",")
	if !strings.Contains(j, "rg|prod|critical|0") || !strings.Contains(j, "rg|prod|critical|1") {
		t.Fatalf("want run and run+summary buttons: %s", j)
	}
}

func TestPromptTextEscapesAndNamesInput(t *testing.T) {
	got := promptText("prod", "deploy-check", "namespace", "required: namespace")
	if !strings.Contains(got, "namespace") || !strings.Contains(got, "required: namespace") {
		t.Fatalf("prompt missing input/err: %q", got)
	}
}

func TestResultTextEscapesHTML(t *testing.T) {
	res := core.RunResult{Summary: "1 < 2 & ok", Phase: "Succeeded"}
	got := resultText("prod", "uc", map[string]string{}, res)
	if strings.Contains(got, "1 < 2 &") || !strings.Contains(got, "1 &lt; 2 &amp; ok") {
		t.Fatalf("summary not HTML-escaped: %q", got)
	}
}

func TestChunk4096(t *testing.T) {
	long := strings.Repeat("x", 4096*2+10)
	parts := chunk4096(long)
	if len(parts) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 4096 {
			t.Fatalf("chunk %d too long: %d", i, len(p))
		}
	}
}

func TestChunk4096RuneSafeAtBoundary(t *testing.T) {
	// "€" is 3 bytes (E2 82 AC); placing it at bytes 4095..4097 means the
	// naive 4096-byte hard cut lands squarely inside the rune.
	input := strings.Repeat("a", 4095) + "€" + strings.Repeat("b", 5000)
	parts := chunk4096(input)
	for i, p := range parts {
		if !utf8.ValidString(p) {
			t.Fatalf("chunk %d is not valid UTF-8: %q", i, p)
		}
		if len(p) > 4096 {
			t.Fatalf("chunk %d too long: %d", i, len(p))
		}
	}
	if joined := strings.Join(parts, ""); joined != input {
		t.Fatalf("chunks are not lossless: got %d bytes, want %d bytes", len(joined), len(input))
	}
}

func TestChunk4096DoesNotSplitHTMLEntity(t *testing.T) {
	// "&amp;" occupies bytes 4094..4098 — the naive 4096-byte hard cut lands
	// squarely inside it (after "&a", before "mp;").
	input := strings.Repeat("a", 4094) + "&amp;" + strings.Repeat("b", 5000)
	parts := chunk4096(input)

	for i, p := range parts {
		if len(p) > 4096 {
			t.Fatalf("chunk %d too long: %d", i, len(p))
		}
		// No chunk may end with an unterminated entity: if it contains a
		// '&', the last '&' must be matched by a ';' at or before the end
		// of the chunk (i.e. the last '&' index <= the last ';' index).
		lastAmp := strings.LastIndexByte(p, '&')
		lastSemi := strings.LastIndexByte(p, ';')
		if lastAmp >= 0 && lastAmp > lastSemi {
			t.Fatalf("chunk %d ends mid-entity: %q", i, p)
		}
	}
	if joined := strings.Join(parts, ""); joined != input {
		t.Fatalf("chunks are not lossless: got %d bytes, want %d bytes", len(joined), len(input))
	}
}
