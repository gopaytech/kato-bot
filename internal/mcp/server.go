// Package mcp is kato-bot's MCP front door: 11 tools over the gateway and
// group API, served via streamable HTTP. Tool results carry kato's JSON
// verbatim as text; failures surface as tool errors with the gateway's
// message.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zufardhiyaulhaq/kato-bot/internal/gateway"
	"github.com/zufardhiyaulhaq/kato-bot/internal/groupapi"
)

// serverName/serverVersion identify kato-bot to MCP clients.
const (
	serverName    = "kato-bot"
	serverVersion = "0.1.5"
)

type listUseCasesIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
}
type getUseCaseIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
	UseCase string `json:"usecase" jsonschema:"use case name (discover via list_usecases)"`
}
type runUseCaseIn struct {
	Cluster        string            `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
	UseCase        string            `json:"usecase" jsonschema:"use case name (discover via list_usecases)"`
	Inputs         map[string]string `json:"inputs,omitempty" jsonschema:"use case inputs; all values are strings"`
	IncludeOutputs bool              `json:"include_outputs,omitempty" jsonschema:"include per-step raw outputs in the response (default false)"`
}
type listMethodsIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
}
type runMethodIn struct {
	Cluster string            `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
	Method  string            `json:"method" jsonschema:"method name (discover via list_methods)"`
	Params  map[string]string `json:"params,omitempty" jsonschema:"method params; all values are strings"`
}
type listRunsIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
	UseCase string `json:"usecase,omitempty" jsonschema:"optional: only runs of this use case"`
}
type getRunIn struct {
	Cluster string `json:"cluster" jsonschema:"cluster name (discover via list_clusters)"`
	Run     string `json:"run" jsonschema:"run name (from run_usecase's run field or list_runs)"`
}

type runGroupIn struct {
	Group   string `json:"group" jsonschema:"the configured group name (discover via list_groups)"`
	Summary bool   `json:"summary,omitempty" jsonschema:"also produce an LLM group summary"`
}

type getGroupRunIn struct {
	RunID string `json:"run_id" jsonschema:"the runId returned by run_group"`
}

// GroupAPI is the non-interactive front door for predefined groups (backed by
// internal/groupapi.Service), kept as an interface here so this package never
// imports internal/platform/lark. Submit launches a group run in the
// background and returns a runId immediately; GetRun polls it.
type GroupAPI interface {
	ListJSON() []byte
	Submit(name string, summary bool) (string, *gateway.Error)
	GetRun(runID string) (*groupapi.RunView, *gateway.Error)
}

// NewServer builds the MCP server with all 11 tools registered over g and groups.
func NewServer(g *gateway.Gateway, groups GroupAPI) *sdkmcp.Server {
	s := sdkmcp.NewServer(&sdkmcp.Implementation{Name: serverName, Version: serverVersion}, nil)

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "list_clusters",
		Description: "List the configured kato clusters (name + label). Call this first: every other tool needs a cluster name.",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return textResult(g.ClustersJSON()), nil, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "list_usecases",
		Description: "List a cluster's kato troubleshooting use cases (name, description, declared inputs, ready).",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in listUseCasesIn) (json.RawMessage, error) {
		return cl.RawListUseCases(ctx)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "get_usecase",
		Description: "Get one use case's contract: description and declared inputs (name, required, default).",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in getUseCaseIn) (json.RawMessage, error) {
		return cl.RawGetUseCase(ctx, in.UseCase)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "run_usecase",
		Description: "Execute a kato use case. SYNCHRONOUS AND SLOW: kato runs every step and writes an LLM summary before responding (tens of seconds to minutes). Returns run name, phase, summary, warning.",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in runUseCaseIn) (json.RawMessage, error) {
		body, err := json.Marshal(map[string]any{"inputs": nonNil(in.Inputs)})
		if err != nil {
			return nil, err
		}
		q := "includeOutputs=false"
		if in.IncludeOutputs {
			q = "includeOutputs=true"
		}
		return cl.RawRunUseCase(ctx, in.UseCase, q, body)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "list_methods",
		Description: "List kato's built-in read-only check methods with their params and output fields. Use before run_method to discover a method's params.",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in listMethodsIn) (json.RawMessage, error) {
		return cl.RawListMethods(ctx)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "run_method",
		Description: "Execute one kato method directly — a fast stateless probe (no run persisted, no LLM). Returns outcome (completed|failed), outputs, and error; a failed outcome is a finding, not a transport failure.",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in runMethodIn) (json.RawMessage, error) {
		body, err := json.Marshal(map[string]any{"params": nonNil(in.Params)})
		if err != nil {
			return nil, err
		}
		return cl.RawRunMethod(ctx, in.Method, body)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "list_runs",
		Description: "List past kato runs (newest first), optionally filtered by use case.",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in listRunsIn) (json.RawMessage, error) {
		q := ""
		if in.UseCase != "" {
			q = "usecase=" + url.QueryEscape(in.UseCase)
		}
		return cl.RawListRuns(ctx, q)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "get_run",
		Description: "Get one past run's full audit record (per-step outputs, summary, timings).",
	}, proxyTool(g, func(ctx context.Context, cl gateway.Client, in getRunIn) (json.RawMessage, error) {
		return cl.RawGetRun(ctx, in.Run)
	}))

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "list_groups",
		Description: "List the predefined groups (name, cluster, usecase, target count). Call this before run_group to discover a group name.",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, _ any) (*sdkmcp.CallToolResult, any, error) {
		return textResult(groups.ListJSON()), nil, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "run_group",
		Description: "Submit a predefined group to run: the configured use case runs across every target in the group's cluster, in the background, decoupled from this call. ASYNC: returns a runId immediately (status \"running\") instead of waiting for the run to finish. Poll get_group_run with the runId for the JSON result. It does not take a cluster — a group pins its own.",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in runGroupIn) (*sdkmcp.CallToolResult, any, error) {
		runID, e := groups.Submit(in.Group, in.Summary)
		if e != nil {
			return nil, nil, e
		}
		raw, err := json.Marshal(map[string]string{"runId": runID, "status": "running"})
		if err != nil {
			return nil, nil, err
		}
		return textResult(raw), nil, nil
	})

	sdkmcp.AddTool(s, &sdkmcp.Tool{
		Name:        "get_group_run",
		Description: "Poll a group run submitted via run_group. Returns status (running|done|failed); when done, per-service results and final tallies; when failed, the error.",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, in getGroupRunIn) (*sdkmcp.CallToolResult, any, error) {
		view, e := groups.GetRun(in.RunID)
		if e != nil {
			return nil, nil, e
		}
		raw, err := json.Marshal(view)
		if err != nil {
			return nil, nil, err
		}
		return textResult(raw), nil, nil
	})

	return s
}

// Handler serves s over streamable HTTP (mounted at /mcp by main).
func Handler(s *sdkmcp.Server) http.Handler {
	return sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return s },
		&sdkmcp.StreamableHTTPOptions{
			// This listener is always-on and unauthenticated; a client that
			// connects and vanishes (crashed agent, dropped port-forward,
			// stray curl) must not leak its session for the pod's lifetime.
			SessionTimeout: 30 * time.Minute,
		},
	)
}

// clusterCarrier lets proxyTool read the cluster field off any input struct.
type clusterCarrier interface{ clusterName() string }

func (i listUseCasesIn) clusterName() string { return i.Cluster }
func (i getUseCaseIn) clusterName() string   { return i.Cluster }
func (i runUseCaseIn) clusterName() string   { return i.Cluster }
func (i listMethodsIn) clusterName() string  { return i.Cluster }
func (i runMethodIn) clusterName() string    { return i.Cluster }
func (i listRunsIn) clusterName() string     { return i.Cluster }
func (i getRunIn) clusterName() string       { return i.Cluster }

// proxyTool resolves the cluster and adapts a gateway call into an SDK tool
// handler. A returned error becomes an MCP tool error (IsError), which is
// exactly the spec's mapping for unknown clusters and upstream failures.
func proxyTool[In clusterCarrier](g *gateway.Gateway, call func(ctx context.Context, cl gateway.Client, in In) (json.RawMessage, error)) sdkmcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, any, error) {
		cl, ok := g.Get(in.clusterName())
		if !ok {
			return nil, nil, gateway.UnknownCluster(in.clusterName())
		}
		raw, err := call(ctx, cl, in)
		if err != nil {
			return nil, nil, gateway.Normalize(in.clusterName(), err)
		}
		return textResult(raw), nil, nil
	}
}

func textResult(raw []byte) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}},
	}
}

// nonNil normalizes a nil map to empty so kato receives {"inputs":{}} not {"inputs":null}.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
