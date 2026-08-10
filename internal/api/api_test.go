package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
	"github.com/zufardhiyaulhaq/kato-bot/internal/gateway"
	"github.com/zufardhiyaulhaq/kato-bot/internal/groupapi"
)

// fakeKato records the last raw call and returns canned bytes or an error.
type fakeKato struct {
	lastCall  string // e.g. "RunMethod pod_status" — method + name
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

	lastSubmit string
	submitID   string
	submitErr  *gateway.Error

	lastGetRun string
	view       *groupapi.RunView
	getRunErr  *gateway.Error
}

func (f *fakeGroupAPI) ListJSON() []byte { return f.listJSON }
func (f *fakeGroupAPI) Submit(name string) (string, *gateway.Error) {
	f.lastSubmit = name
	return f.submitID, f.submitErr
}
func (f *fakeGroupAPI) GetRun(runID string) (*groupapi.RunView, *gateway.Error) {
	f.lastGetRun = runID
	return f.view, f.getRunErr
}

func newAPI(fake *fakeKato) http.Handler {
	return newAPIWithGroups(fake, &fakeGroupAPI{listJSON: []byte(`{"groups":[]}`)})
}

func newAPIWithGroups(fake *fakeKato, groups GroupAPI) http.Handler {
	g := gateway.New()
	g.Add(core.Cluster{Name: "prod", Label: "Production"}, fake)
	mux := http.NewServeMux()
	Register(mux, g, groups)
	return mux
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestClustersEndpoint(t *testing.T) {
	w := do(newAPI(&fakeKato{}), "GET", "/api/v1/clusters", "")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	want := `{"clusters":[{"name":"prod","label":"Production"}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// Every proxy route forwards to the right client call and returns kato's
// bytes verbatim with pass-through of query and body.
func TestRoutes_VerbatimPassthrough(t *testing.T) {
	const katoJSON = `{"anything":["verbatim",{"Name":"CapitalizedWartKept"}]}`
	tests := []struct {
		name, method, target, body string
		wantCall, wantQuery        string
		wantBody                   string
	}{
		{"list usecases", "GET", "/api/v1/clusters/prod/usecases", "", "ListUseCases", "", ""},
		{"get usecase", "GET", "/api/v1/clusters/prod/usecases/pod-x", "", "GetUseCase pod-x", "", ""},
		{"run usecase", "POST", "/api/v1/clusters/prod/usecases/pod-x/run?includeOutputs=true",
			`{"inputs":{"ns":"a"}}`, "RunUseCase pod-x", "includeOutputs=true", `{"inputs":{"ns":"a"}}`},
		{"list methods", "GET", "/api/v1/clusters/prod/methods", "", "ListMethods", "", ""},
		{"run method", "POST", "/api/v1/clusters/prod/methods/pod_status/run",
			`{"params":{"pod":"x"}}`, "RunMethod pod_status", "", `{"params":{"pod":"x"}}`},
		{"list runs", "GET", "/api/v1/clusters/prod/runs?usecase=pod-x", "", "ListRuns", "usecase=pod-x", ""},
		{"get run", "GET", "/api/v1/clusters/prod/runs/run-1", "", "GetRun run-1", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeKato{resp: json.RawMessage(katoJSON)}
			w := do(newAPI(fake), tc.method, tc.target, tc.body)
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			if w.Body.String() != katoJSON {
				t.Errorf("body = %s, want verbatim kato bytes", w.Body.String())
			}
			if fake.lastCall != tc.wantCall {
				t.Errorf("call = %q, want %q", fake.lastCall, tc.wantCall)
			}
			if fake.lastQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", fake.lastQuery, tc.wantQuery)
			}
			if string(fake.lastBody) != tc.wantBody {
				t.Errorf("body forwarded = %q, want %q", fake.lastBody, tc.wantBody)
			}
		})
	}
}

func TestUnknownCluster404(t *testing.T) {
	w := do(newAPI(&fakeKato{}), "GET", "/api/v1/clusters/nope/usecases", "")
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if want := `{"error":"unknown cluster \"nope\""}`; strings.TrimSpace(w.Body.String()) != want {
		t.Errorf("body = %s, want %s", w.Body.String(), want)
	}
}

// kato non-2xx passes through status + {"error"} body; transport error is 502.
func TestUpstreamErrorMapping(t *testing.T) {
	t.Run("kato 429 passthrough", func(t *testing.T) {
		fake := &fakeKato{err: &apiErr429{}}
		w := do(newAPI(fake), "GET", "/api/v1/clusters/prod/usecases", "")
		if w.Code != 429 {
			t.Fatalf("status = %d, want 429", w.Code)
		}
		if want := `{"error":"kato is busy"}`; strings.TrimSpace(w.Body.String()) != want {
			t.Errorf("body = %s, want %s", w.Body.String(), want)
		}
	})
	t.Run("transport 502", func(t *testing.T) {
		fake := &fakeKato{err: errors.New("dial tcp: refused")}
		w := do(newAPI(fake), "GET", "/api/v1/clusters/prod/usecases", "")
		if w.Code != 502 {
			t.Fatalf("status = %d, want 502", w.Code)
		}
		if !strings.Contains(w.Body.String(), `cluster \"prod\" unreachable`) {
			t.Errorf("body = %s, want unreachable message", w.Body.String())
		}
	})
}

func TestGroupsEndpoint(t *testing.T) {
	groups := &fakeGroupAPI{listJSON: []byte(`{"groups":[{"name":"g1","cluster":"prod","usecase":"dt","targets":1}]}`)}
	w := do(newAPIWithGroups(&fakeKato{}, groups), "GET", "/api/v1/groups", "")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != string(groups.listJSON) {
		t.Errorf("body = %s, want %s", got, groups.listJSON)
	}
}

func TestGroupRunEndpoint_Submitted(t *testing.T) {
	groups := &fakeGroupAPI{submitID: "run-abc123"}
	w := do(newAPIWithGroups(&fakeKato{}, groups), "POST", "/api/v1/groups/g1/run", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, w.Body.String())
	}
	if got["runId"] != "run-abc123" {
		t.Errorf("body runId = %v, want run-abc123", got["runId"])
	}
	if got["group"] != "g1" {
		t.Errorf("body group = %v, want g1", got["group"])
	}
	if got["status"] != "running" {
		t.Errorf("body status = %v, want running", got["status"])
	}
	if groups.lastSubmit != "g1" {
		t.Errorf("Submit called with %q, want g1", groups.lastSubmit)
	}
}

func TestGroupRunEndpoint_Errors(t *testing.T) {
	tests := []struct {
		name string
		err  *gateway.Error
		want int
	}{
		{"unknown group", &gateway.Error{Status: 404, Msg: "unknown group nope"}, 404},
		{"already running", &gateway.Error{Status: 409, Msg: "group nope is already running"}, 409},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups := &fakeGroupAPI{submitErr: tc.err}
			w := do(newAPIWithGroups(&fakeKato{}, groups), "POST", "/api/v1/groups/nope/run", "")
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestGetGroupRunEndpoint_OK(t *testing.T) {
	view := &groupapi.RunView{
		RunID: "run-1", Group: "g1", Cluster: "prod", UseCase: "dt",
		Status: "done", StartedAt: "2026-08-07T00:00:00Z", CompletedAt: "2026-08-07T00:01:00Z",
		Result: &groupapi.GroupResult{
			Group: "g1", Cluster: "prod", UseCase: "dt",
			Tallies:  groupapi.Tallies{Healthy: 1, Total: 1},
			Services: []groupapi.ServiceView{{Target: map[string]string{"deployment": "x"}}},
		},
	}
	groups := &fakeGroupAPI{view: view}
	w := do(newAPIWithGroups(&fakeKato{}, groups), "GET", "/api/v1/groups/runs/run-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var got groupapi.RunView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not valid JSON: %v (%s)", err, w.Body.String())
	}
	if got.RunID != "run-1" || got.Status != "done" || got.Result == nil {
		t.Errorf("view = %+v, want run-1/done with Result", got)
	}
	if groups.lastGetRun != "run-1" {
		t.Errorf("GetRun called with %q, want run-1", groups.lastGetRun)
	}
}

func TestGetGroupRunEndpoint_NotFound(t *testing.T) {
	groups := &fakeGroupAPI{getRunErr: &gateway.Error{Status: 404, Msg: "unknown run nope"}}
	w := do(newAPIWithGroups(&fakeKato{}, groups), "GET", "/api/v1/groups/runs/nope", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

// apiErr429 implements core.HTTPStatusError like kato.APIError does.
type apiErr429 struct{}

func (*apiErr429) Error() string   { return "kato 429: kato is busy" }
func (*apiErr429) HTTPStatus() int { return 429 }
func (*apiErr429) Detail() string  { return "kato is busy" }
