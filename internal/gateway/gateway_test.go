package gateway

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/kato"
)

// *kato.Client must satisfy gateway.Client (compile-time proof the Raw surface matches).
var _ Client = (*kato.Client)(nil)

// fakeClient satisfies Client; only the methods a test calls matter.
type fakeClient struct{ Client }

func TestClustersJSON_SpecShape(t *testing.T) {
	g := New()
	g.Add(core.Cluster{Name: "prod", Label: "Production"}, fakeClient{})
	g.Add(core.Cluster{Name: "staging"}, fakeClient{})

	got := string(g.ClustersJSON())
	want := `{"clusters":[{"name":"prod","label":"Production"},{"name":"staging"}]}`
	if got != want {
		t.Errorf("ClustersJSON = %s, want %s (label omitted when empty, insertion order)", got, want)
	}
}

func TestGet(t *testing.T) {
	g := New()
	g.Add(core.Cluster{Name: "prod"}, fakeClient{})
	if _, ok := g.Get("prod"); !ok {
		t.Error("Get(prod) = miss, want hit")
	}
	if _, ok := g.Get("nope"); ok {
		t.Error("Get(nope) = hit, want miss")
	}
}

func TestClusters_IsACopy(t *testing.T) {
	g := New()
	g.Add(core.Cluster{Name: "prod"}, fakeClient{})
	list := g.Clusters()
	list[0].Name = "mutated"
	if g.Clusters()[0].Name != "prod" {
		t.Error("Clusters() must return a copy")
	}
}

// Error normalization: kato *APIError (via core.HTTPStatusError) passes its
// status + message through; anything else is 502 unreachable.
func TestNormalize(t *testing.T) {
	apiErr := &kato.APIError{Status: 429, Msg: "too many concurrent method runs"}
	if e := Normalize("prod", apiErr); e.Status != 429 || e.Msg != "too many concurrent method runs" {
		t.Errorf("Normalize(APIError) = %d %q, want 429 passthrough", e.Status, e.Msg)
	}
	if e := Normalize("prod", errors.New("dial tcp: connection refused")); e.Status != 502 {
		t.Errorf("Normalize(transport) status = %d, want 502", e.Status)
	} else if e.Msg != `cluster "prod" unreachable: dial tcp: connection refused` {
		t.Errorf("Normalize(transport) msg = %q", e.Msg)
	}
}

func TestUnknownCluster(t *testing.T) {
	e := UnknownCluster("x")
	if e.Status != 404 || e.Msg != `unknown cluster "x"` {
		t.Errorf("UnknownCluster = %d %q", e.Status, e.Msg)
	}
}

// ClustersJSON output must be stable, valid JSON even with zero clusters.
func TestClustersJSON_Empty(t *testing.T) {
	var v struct {
		Clusters []core.Cluster `json:"clusters"`
	}
	if err := json.Unmarshal(New().ClustersJSON(), &v); err != nil {
		t.Fatalf("empty ClustersJSON invalid: %v", err)
	}
	if len(v.Clusters) != 0 {
		t.Errorf("want zero clusters, got %v", v.Clusters)
	}
}

// A transport *url.Error must not leak the internal kato URL into the message.
func TestNormalize_URLErrorDoesNotLeakURL(t *testing.T) {
	ue := &url.Error{Op: "Get", URL: "http://kato.prod.svc:8080/api/v1/usecases", Err: errors.New("dial tcp: connection refused")}
	e := Normalize("prod", ue)
	if e.Status != 502 {
		t.Fatalf("status = %d, want 502", e.Status)
	}
	if strings.Contains(e.Msg, "kato.prod.svc") {
		t.Errorf("msg leaks internal URL: %q", e.Msg)
	}
	if want := `cluster "prod" unreachable: dial tcp: connection refused`; e.Msg != want {
		t.Errorf("msg = %q, want %q", e.Msg, want)
	}
}

// Transport errors must not leak internal addressing: *net.OpError embeds the
// dial ip:port and *net.DNSError the hostname; both are reduced to leaf causes.
func TestNormalize_NetErrorsDoNotLeakAddresses(t *testing.T) {
	t.Run("op error strips ip:port", func(t *testing.T) {
		ue := &url.Error{Op: "Get", URL: "http://kato.prod.svc:8080/api/v1/usecases", Err: &net.OpError{
			Op: "dial", Net: "tcp",
			Addr: &net.TCPAddr{IP: net.IPv4(10, 42, 3, 7), Port: 8080},
			Err:  errors.New("connect: connection refused"),
		}}
		e := Normalize("prod", ue)
		if e.Status != 502 {
			t.Fatalf("status = %d, want 502", e.Status)
		}
		for _, leak := range []string{"10.42.3.7", "8080", "kato.prod.svc"} {
			if strings.Contains(e.Msg, leak) {
				t.Errorf("msg leaks %q: %q", leak, e.Msg)
			}
		}
		if want := `cluster "prod" unreachable: connect: connection refused`; e.Msg != want {
			t.Errorf("msg = %q, want %q", e.Msg, want)
		}
	})
	t.Run("dns error strips hostname", func(t *testing.T) {
		ue := &url.Error{Op: "Get", URL: "http://kato.prod.svc:8080/api/v1/usecases", Err: &net.OpError{
			Op: "dial", Net: "tcp",
			Err: &net.DNSError{Err: "no such host", Name: "kato.prod.svc.cluster.local"},
		}}
		e := Normalize("prod", ue)
		if strings.Contains(e.Msg, "kato.prod.svc") {
			t.Errorf("msg leaks hostname: %q", e.Msg)
		}
		if want := `cluster "prod" unreachable: dns lookup failed: no such host`; e.Msg != want {
			t.Errorf("msg = %q, want %q", e.Msg, want)
		}
	})
}
