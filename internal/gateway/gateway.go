// Package gateway is the aggregation layer shared by kato-bot's REST proxy and
// MCP server: it resolves a cluster name to its kato client and normalizes
// upstream failures. Both front doors are thin translations of this package.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/gopaytech/kato-bot/internal/core"
)

// Client is the raw kato surface the gateway proxies (implemented by
// *kato.Client's Raw* methods). Consumer-side interface: core.KatoClient
// stays narrow for the Lark flow; this one covers the full kato API.
type Client interface {
	RawListUseCases(ctx context.Context) (json.RawMessage, error)
	RawGetUseCase(ctx context.Context, name string) (json.RawMessage, error)
	RawRunUseCase(ctx context.Context, name, rawQuery string, body []byte) (json.RawMessage, error)
	RawListMethods(ctx context.Context) (json.RawMessage, error)
	RawRunMethod(ctx context.Context, name string, body []byte) (json.RawMessage, error)
	RawListRuns(ctx context.Context, rawQuery string) (json.RawMessage, error)
	RawGetRun(ctx context.Context, name string) (json.RawMessage, error)
}

// Gateway maps cluster names to clients, preserving configuration order.
// Built once at startup, read-only thereafter (safe for concurrent reads).
type Gateway struct {
	order   []core.Cluster
	clients map[string]Client
}

func New() *Gateway {
	return &Gateway{clients: map[string]Client{}}
}

// Add registers a cluster; a duplicate name overwrites the client without
// duplicating the ordered entry (mirrors core.Registry semantics).
func (g *Gateway) Add(c core.Cluster, cl Client) {
	if _, exists := g.clients[c.Name]; !exists {
		g.order = append(g.order, c)
	}
	g.clients[c.Name] = cl
}

// Clusters returns the registered clusters in insertion order (a copy).
func (g *Gateway) Clusters() []core.Cluster {
	out := make([]core.Cluster, len(g.order))
	copy(out, g.order)
	return out
}

// Get resolves the client for a cluster name.
func (g *Gateway) Get(name string) (Client, bool) {
	c, ok := g.clients[name]
	return c, ok
}

// clusterView is the wire shape of one cluster: name + optional label, never the URL.
type clusterView struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
}

// ClustersJSON renders the spec's cluster listing: insertion order, label
// omitted when empty. Marshaling a []clusterView cannot fail.
func (g *Gateway) ClustersJSON() []byte {
	views := make([]clusterView, 0, len(g.order))
	for _, c := range g.order {
		views = append(views, clusterView{Name: c.Name, Label: c.Label})
	}
	b, _ := json.Marshal(map[string]any{"clusters": views})
	return b
}

// Error is a normalized proxy failure: the HTTP status the REST front door
// returns and the message both front doors surface.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// UnknownCluster is the 404 for a cluster name not in the gateway.
func UnknownCluster(name string) *Error {
	return &Error{Status: http.StatusNotFound, Msg: fmt.Sprintf("unknown cluster %q", name)}
}

// Normalize maps an upstream call failure: a kato status-bearing error passes
// its status + message through verbatim; anything else (transport, timeout)
// is a 502 naming the unreachable cluster. Transport errors are unwrapped
// first so the internal kato URL, resolved ip:port, and hostname never leak
// into client-facing messages (spec: the kato URL stays internal).
func Normalize(cluster string, err error) *Error {
	var se core.HTTPStatusError
	if errors.As(err, &se) {
		return &Error{Status: se.HTTPStatus(), Msg: se.Detail()}
	}
	// Strip internal addressing from transport errors before they reach
	// clients (spec: the kato URL stays internal): *url.Error embeds the full
	// URL, *net.OpError the resolved ip:port, *net.DNSError the hostname.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var de *net.DNSError
	var oe *net.OpError
	if errors.As(err, &de) {
		// DNSError.Error() includes the looked-up name; DNSError.Err does not.
		err = fmt.Errorf("dns lookup failed: %s", de.Err)
	} else if errors.As(err, &oe) && oe.Err != nil {
		// OpError.Error() includes the dial address; its inner error does not.
		err = oe.Err
	}
	return &Error{Status: http.StatusBadGateway, Msg: fmt.Sprintf("cluster %q unreachable: %v", cluster, err)}
}
