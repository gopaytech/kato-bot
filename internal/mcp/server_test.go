package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
	"github.com/zufardhiyaulhaq/kato-bot/internal/gateway"
	"github.com/zufardhiyaulhaq/kato-bot/internal/groupapi"
)

// fakeKato mirrors internal/api's fake: records the last call, returns canned bytes.
type fakeKato struct {
	lastCall  string
	lastQuery string
	lastBody  []byte
	resp      json.RawMessage
	err       error
}

func (f *fakeKato) ret() (json.RawMessage, error) { return f.resp, f.err }
func (f *fakeKato) RawListUseCases(context.Context) (json.RawMessage, error) {
	f.lastCall = "ListUseCases"
	return f.ret()
}
func (f *fakeKato) RawGetUseCase(_ context.Context, name string) (json.RawMessage, error) {
	f.lastCall = "GetUseCase " + name
	return f.ret()
}
func (f *fakeKato) RawRunUseCase(_ context.Context, name, rawQuery string, body []byte) (json.RawMessage, error) {
	f.lastCall, f.lastQuery, f.lastBody = "RunUseCase "+name, rawQuery, body
	return f.ret()
}
func (f *fakeKato) RawListMethods(context.Context) (json.RawMessage, error) {
	f.lastCall = "ListMethods"
	return f.ret()
}
func (f *fakeKato) RawRunMethod(_ context.Context, name string, body []byte) (json.RawMessage, error) {
	f.lastCall, f.lastBody = "RunMethod "+name, body
	return f.ret()
}
func (f *fakeKato) RawListRuns(_ context.Context, rawQuery string) (json.RawMessage, error) {
	f.lastCall, f.lastQuery = "ListRuns", rawQuery
	return f.ret()
}
func (f *fakeKato) RawGetRun(_ context.Context, name string) (json.RawMessage, error) {
	f.lastCall = "GetRun " + name
	return f.ret()
}

// fakeGroupAPI is a minimal GroupAPI stand-in that records the last
// Submit/GetRun call.
type fakeGroupAPI struct {
	listJSON []byte

	lastSubmit        string
	lastSubmitSummary bool
	submitID          string
	submitErr         *gateway.Error

	lastGetRun string
	view       *groupapi.RunView
	getRunErr  *gateway.Error
}

func (f *fakeGroupAPI) ListJSON() []byte { return f.listJSON }
func (f *fakeGroupAPI) Submit(name string, summary bool) (string, *gateway.Error) {
	f.lastSubmit = name
	f.lastSubmitSummary = summary
	return f.submitID, f.submitErr
}
func (f *fakeGroupAPI) GetRun(runID string) (*groupapi.RunView, *gateway.Error) {
	f.lastGetRun = runID
	return f.view, f.getRunErr
}

// session spins up the MCP server over in-memory transports and returns a
// connected client session.
func session(t *testing.T, fake *fakeKato) *sdkmcp.ClientSession {
	t.Helper()
	return sessionWithGroups(t, fake, &fakeGroupAPI{listJSON: []byte(`{"groups":[]}`)})
}

func sessionWithGroups(t *testing.T, fake *fakeKato, groups GroupAPI) *sdkmcp.ClientSession {
	t.Helper()
	g := gateway.New()
	g.Add(core.Cluster{Name: "prod", Label: "Production"}, fake)
	srv := NewServer(g, groups)

	st, ct := sdkmcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// textOf returns the concatenated text content of a result.
func textOf(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func call(t *testing.T, cs *sdkmcp.ClientSession, tool string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	return res
}

// All 11 tools are registered.
func TestToolsRegistered(t *testing.T) {
	cs := session(t, &fakeKato{resp: json.RawMessage(`{}`)})
	tools, err := cs.ListTools(context.Background(), &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		"list_clusters": false, "list_usecases": false, "get_usecase": false,
		"run_usecase": false, "list_methods": false, "run_method": false,
		"list_runs": false, "get_run": false,
		"list_groups": false, "run_group": false, "get_group_run": false,
	}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		} else {
			t.Errorf("unexpected tool %q", tool.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestListClusters(t *testing.T) {
	cs := session(t, &fakeKato{})
	res := call(t, cs, "list_clusters", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	want := `{"clusters":[{"name":"prod","label":"Production"}]}`
	if got := textOf(t, res); got != want {
		t.Errorf("text = %s, want %s", got, want)
	}
}

// A proxy tool returns kato's JSON verbatim as text.
func TestListUseCases_Verbatim(t *testing.T) {
	const katoJSON = `{"usecases":[{"name":"pod-x","ready":true}]}`
	fake := &fakeKato{resp: json.RawMessage(katoJSON)}
	cs := session(t, fake)
	res := call(t, cs, "list_usecases", map[string]any{"cluster": "prod"})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	if got := textOf(t, res); got != katoJSON {
		t.Errorf("text = %s, want verbatim %s", got, katoJSON)
	}
	if fake.lastCall != "ListUseCases" {
		t.Errorf("call = %q", fake.lastCall)
	}
}

// run_method marshals params into kato's body shape and names the method.
func TestRunMethod_Plumbing(t *testing.T) {
	fake := &fakeKato{resp: json.RawMessage(`{"outcome":"completed","outputs":{}}`)}
	cs := session(t, fake)
	res := call(t, cs, "run_method", map[string]any{
		"cluster": "prod", "method": "pod_status",
		"params": map[string]any{"namespace": "payments", "pod": "api-0"},
	})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	if fake.lastCall != "RunMethod pod_status" {
		t.Errorf("call = %q, want RunMethod pod_status", fake.lastCall)
	}
	var body struct {
		Params map[string]string `json:"params"`
	}
	if err := json.Unmarshal(fake.lastBody, &body); err != nil {
		t.Fatalf("body sent: %s (%v)", fake.lastBody, err)
	}
	if body.Params["namespace"] != "payments" || body.Params["pod"] != "api-0" {
		t.Errorf("params = %v", body.Params)
	}
}

// run_usecase: include_outputs=false (default) → includeOutputs=false query;
// true → includeOutputs=true; inputs marshaled into kato's body shape.
func TestRunUseCase_Plumbing(t *testing.T) {
	fake := &fakeKato{resp: json.RawMessage(`{"run":"r1","phase":"Succeeded"}`)}
	cs := session(t, fake)
	res := call(t, cs, "run_usecase", map[string]any{
		"cluster": "prod", "usecase": "pod-x",
		"inputs": map[string]any{"namespace": "payments"},
	})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	if fake.lastCall != "RunUseCase pod-x" {
		t.Errorf("call = %q", fake.lastCall)
	}
	if fake.lastQuery != "includeOutputs=false" {
		t.Errorf("query = %q, want includeOutputs=false (default)", fake.lastQuery)
	}
	call(t, cs, "run_usecase", map[string]any{
		"cluster": "prod", "usecase": "pod-x", "include_outputs": true,
	})
	if fake.lastQuery != "includeOutputs=true" {
		t.Errorf("query = %q, want includeOutputs=true", fake.lastQuery)
	}
}

// list_runs forwards the optional usecase filter.
func TestListRuns_Filter(t *testing.T) {
	fake := &fakeKato{resp: json.RawMessage(`{"runs":[]}`)}
	cs := session(t, fake)
	call(t, cs, "list_runs", map[string]any{"cluster": "prod"})
	if fake.lastQuery != "" {
		t.Errorf("query = %q, want empty when no filter", fake.lastQuery)
	}
	call(t, cs, "list_runs", map[string]any{"cluster": "prod", "usecase": "pod x"})
	if fake.lastQuery != "usecase=pod+x" {
		t.Errorf("query = %q, want usecase=pod+x (escaped)", fake.lastQuery)
	}
}

// Unknown cluster and upstream failures surface as tool errors with the
// gateway's message, never as protocol errors.
func TestToolErrors(t *testing.T) {
	cs := session(t, &fakeKato{err: apiErr429{}})
	res := call(t, cs, "list_usecases", map[string]any{"cluster": "nope"})
	if !res.IsError || !strings.Contains(textOf(t, res), `unknown cluster "nope"`) {
		t.Errorf("unknown cluster: IsError=%v content=%s", res.IsError, textOf(t, res))
	}
	res = call(t, cs, "list_usecases", map[string]any{"cluster": "prod"})
	if !res.IsError || !strings.Contains(textOf(t, res), "kato is busy") {
		t.Errorf("upstream error: IsError=%v content=%s", res.IsError, textOf(t, res))
	}
}

// list_groups returns the group API's JSON verbatim as text.
func TestListGroups_Verbatim(t *testing.T) {
	const groupsJSON = `{"groups":[{"name":"g1","cluster":"prod","usecase":"dt","targets":2}]}`
	groups := &fakeGroupAPI{listJSON: json.RawMessage(groupsJSON)}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "list_groups", map[string]any{})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	if got := textOf(t, res); got != groupsJSON {
		t.Errorf("text = %s, want verbatim %s", got, groupsJSON)
	}
}

// run_group takes no cluster, calls groups.Submit, and returns a runId + running status.
func TestRunGroup_ReturnsRunID(t *testing.T) {
	groups := &fakeGroupAPI{submitID: "run-abc123"}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "run_group", map[string]any{"group": "g1"})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("text not valid JSON: %v (%s)", err, textOf(t, res))
	}
	if got["runId"] != "run-abc123" {
		t.Errorf("result runId = %v, want run-abc123", got["runId"])
	}
	if got["status"] != "running" {
		t.Errorf("result status = %v, want running", got["status"])
	}
	if groups.lastSubmit != "g1" {
		t.Errorf("Submit called with %q, want g1", groups.lastSubmit)
	}
	if groups.lastSubmitSummary {
		t.Errorf("Submit called with summary=true, want false (no Summary input)")
	}
}

// run_group forwards its Summary input field to GroupAPI.Submit's second argument.
func TestRunGroup_SummaryFlagForwarded(t *testing.T) {
	groups := &fakeGroupAPI{submitID: "run-abc123"}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "run_group", map[string]any{"group": "g1", "summary": true})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	if !groups.lastSubmitSummary {
		t.Errorf("Submit called with summary=false, want true (Summary:true input)")
	}
}

// run_group surfaces a *gateway.Error from groups.Submit as a tool error.
func TestRunGroup_Error(t *testing.T) {
	groups := &fakeGroupAPI{submitErr: &gateway.Error{Status: 404, Msg: "unknown group nope"}}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "run_group", map[string]any{"group": "nope"})
	if !res.IsError || !strings.Contains(textOf(t, res), "unknown group nope") {
		t.Errorf("IsError=%v content=%s", res.IsError, textOf(t, res))
	}
}

// get_group_run calls groups.GetRun and returns the RunView JSON on success.
func TestGetGroupRun_ReturnsView(t *testing.T) {
	view := &groupapi.RunView{
		RunID: "run-1", Group: "g1", Cluster: "prod",
		Status: "done", StartedAt: "2026-08-07T00:00:00Z", CompletedAt: "2026-08-07T00:01:00Z",
		Result: &groupapi.GroupResult{
			Group: "g1", Cluster: "prod",
			Tallies:  groupapi.Tallies{Healthy: 1, Total: 1},
			Services: []groupapi.ServiceView{{UseCase: "dt", Target: map[string]string{"deployment": "x"}}},
		},
	}
	groups := &fakeGroupAPI{view: view}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "get_group_run", map[string]any{"run_id": "run-1"})
	if res.IsError {
		t.Fatalf("IsError, content: %s", textOf(t, res))
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(textOf(t, res)), &got); err != nil {
		t.Fatalf("text not valid JSON: %v (%s)", err, textOf(t, res))
	}
	if got["runId"] != "run-1" || got["status"] != "done" {
		t.Errorf("view = %v, want run-1/done", got)
	}
	if groups.lastGetRun != "run-1" {
		t.Errorf("GetRun called with %q, want run-1", groups.lastGetRun)
	}
}

// get_group_run surfaces a *gateway.Error from groups.GetRun (unknown runId) as a tool error.
func TestGetGroupRun_NotFound(t *testing.T) {
	groups := &fakeGroupAPI{getRunErr: &gateway.Error{Status: 404, Msg: "unknown run nope"}}
	cs := sessionWithGroups(t, &fakeKato{}, groups)
	res := call(t, cs, "get_group_run", map[string]any{"run_id": "nope"})
	if !res.IsError || !strings.Contains(textOf(t, res), "unknown run nope") {
		t.Errorf("IsError=%v content=%s", res.IsError, textOf(t, res))
	}
}

type apiErr429 struct{}

func (apiErr429) Error() string   { return "kato 429: kato is busy" }
func (apiErr429) HTTPStatus() int { return 429 }
func (apiErr429) Detail() string  { return "kato is busy" }
