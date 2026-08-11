// internal/summary/summary_test.go
package summary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

type fakeClient struct {
	gotSystem, gotUser string
	out                string
	err                error
}

func (f *fakeClient) Complete(ctx context.Context, system, user string) (string, error) {
	f.gotSystem, f.gotUser = system, user
	return f.out, f.err
}

func boolPtr(b bool) *bool { return &b }

func results() []core.ServiceResult {
	f := false
	return []core.ServiceResult{
		{UseCase: "dt", Target: map[string]string{"namespace": "payments", "deployment": "payment-api"}, Healthy: boolPtr(f), Headline: "CrashLoopBackOff", Summary: "pods crashing on bad image"},
		{UseCase: "http", Target: map[string]string{"target": "api"}, Healthy: boolPtr(true), Headline: "ok"},
	}
}

func TestSummarizeCallsClientWithBoundedEvidence(t *testing.T) {
	c := &fakeClient{out: "18/20 healthy; payment-api crashlooping."}
	g := core.Group{Name: "critical", Cluster: "prod-1"}
	sum, warn := Summarize(context.Background(), c, g, results(), DefaultMaxEvidenceBytes)
	if warn != "" {
		t.Fatalf("warning = %q, want empty", warn)
	}
	if sum != "18/20 healthy; payment-api crashlooping." {
		t.Fatalf("summary = %q", sum)
	}
	// The evidence must include each service line, and the non-healthy service's summary.
	if !strings.Contains(c.gotUser, "payments/payment-api") || !strings.Contains(c.gotUser, "CrashLoopBackOff") {
		t.Errorf("evidence missing unhealthy line: %s", c.gotUser)
	}
	if !strings.Contains(c.gotUser, "pods crashing on bad image") {
		t.Errorf("evidence should include the non-healthy per-service summary: %s", c.gotUser)
	}
	if !strings.Contains(c.gotSystem, "SRE") {
		t.Errorf("system prompt missing: %s", c.gotSystem)
	}
}

func TestSummarizeNilClientIsNotConfigured(t *testing.T) {
	sum, warn := Summarize(context.Background(), nil, core.Group{}, results(), DefaultMaxEvidenceBytes)
	if sum != "" || warn == "" {
		t.Fatalf("nil client should yield empty summary + a warning, got sum=%q warn=%q", sum, warn)
	}
}

func TestSummarizeClientErrorIsNonFatalWarning(t *testing.T) {
	c := &fakeClient{err: errors.New("LLM down")}
	sum, warn := Summarize(context.Background(), c, core.Group{}, results(), DefaultMaxEvidenceBytes)
	if sum != "" || !strings.Contains(warn, "LLM down") {
		t.Fatalf("client error should yield empty summary + warning carrying the error, got sum=%q warn=%q", sum, warn)
	}
}

func TestSummarizeBlankCompletionYieldsWarning(t *testing.T) {
	c := &fakeClient{out: "   \n"}
	sum, warn := Summarize(context.Background(), c, core.Group{}, results(), DefaultMaxEvidenceBytes)
	if sum != "" || warn == "" {
		t.Fatalf("blank completion should yield empty summary + a non-empty warning, got sum=%q warn=%q", sum, warn)
	}
}

func TestBuildEvidenceCapsBytes(t *testing.T) {
	f := false
	var many []core.ServiceResult
	for i := 0; i < 500; i++ {
		many = append(many, core.ServiceResult{UseCase: "dt", Target: map[string]string{"deployment": "d"}, Healthy: &f, Headline: "bad", Summary: strings.Repeat("x", 200)})
	}
	ev := BuildEvidence(many, 4096)
	if len(ev) > 4096+64 { // small allowance for the truncation marker line
		t.Fatalf("evidence not capped: %d bytes", len(ev))
	}
}

func TestBuildEvidenceOversizedItemDoesNotStarveLaterServices(t *testing.T) {
	f := false
	results := []core.ServiceResult{
		{UseCase: "dt", Target: map[string]string{"namespace": "ns1", "deployment": "big-offender"}, Healthy: &f, Headline: "bad", Summary: strings.Repeat("x", 5000)},
		{UseCase: "dt", Target: map[string]string{"namespace": "ns2", "deployment": "later-service"}, Healthy: &f, Headline: "also bad", Summary: "small summary"},
	}
	ev := BuildEvidence(results, 4096)
	if !strings.Contains(ev, "ns2/later-service") {
		t.Fatalf("oversized first item starved later service's header: %s", ev)
	}
	if !strings.Contains(ev, "…") {
		t.Fatalf("expected the oversized summary to be head/tail truncated with an ellipsis: %s", ev)
	}
	for _, line := range strings.Split(ev, "\n") {
		if len(line) > 500 {
			t.Fatalf("found an unbounded line (%d bytes): %q", len(line), line)
		}
	}
}
