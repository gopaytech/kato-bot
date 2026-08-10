package kato

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(srv.URL, 5*time.Second, false)
}

func TestListUseCases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/usecases" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"usecases":[
			{"name":"pod-crashloop","description":"Diagnose crashloop","inputs":[{"name":"ns","required":true}],"ready":true},
			{"name":"broken","description":"x","ready":false}
		]}`))
	}))
	defer srv.Close()

	ucs, err := newClient(t, srv).ListUseCases(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ucs) != 2 {
		t.Fatalf("len = %d", len(ucs))
	}
	if ucs[0].Name != "pod-crashloop" || !ucs[0].Ready || ucs[0].Description != "Diagnose crashloop" {
		t.Errorf("uc0 = %+v", ucs[0])
	}
	if ucs[1].Ready {
		t.Errorf("uc1 should be not ready")
	}
}

func TestGetUseCase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/usecases/pod-crashloop" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"name":"pod-crashloop","description":"d","inputs":[
			{"name":"namespace","required":true},{"name":"pod","required":true},{"name":"tail","required":false}
		],"ready":true}`))
	}))
	defer srv.Close()

	c, err := newClient(t, srv).GetUseCase(context.Background(), "pod-crashloop")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if c.Name != "pod-crashloop" || len(c.Inputs) != 3 {
		t.Fatalf("contract = %+v", c)
	}
	if !c.Inputs[0].Required || c.Inputs[2].Required {
		t.Errorf("required flags wrong: %+v", c.Inputs)
	}
}

func TestGetUseCaseNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"use case not found"}`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv).GetUseCase(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("want APIError 404, got %v", err)
	}
}

func TestRunCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/usecases/pod-crashloop/run" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var req struct {
			Inputs map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if req.Inputs["namespace"] != "payments" {
			t.Errorf("inputs = %+v", req.Inputs)
		}
		w.Write([]byte(`{"run":"pod-crashloop-abc","phase":"Completed","summary":"all good","warning":""}`))
	}))
	defer srv.Close()

	res, err := newClient(t, srv).Run(context.Background(), "pod-crashloop",
		map[string]string{"namespace": "payments", "pod": "x"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Run != "pod-crashloop-abc" || res.Phase != "Completed" || res.Summary != "all good" {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunInputError400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"input pod is required"}`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv).Run(context.Background(), "pod-crashloop", map[string]string{})
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != 400 || apiErr.Msg != "input pod is required" {
		t.Fatalf("want APIError 400 with msg, got %v", err)
	}
}

func TestRunBusy429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error":"too many concurrent runs"}`))
	}))
	defer srv.Close()

	_, err := newClient(t, srv).Run(context.Background(), "x", nil)
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != 429 {
		t.Fatalf("want 429, got %v", err)
	}
}

func TestRunParsesVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"run":"r1","phase":"Succeeded","summary":"bad","healthy":false,"headline":"CrashLoopBackOff"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second, false)
	res, err := c.Run(context.Background(), "uc", map[string]string{"namespace": "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Healthy == nil || *res.Healthy != false {
		t.Errorf("Healthy = %v, want false", res.Healthy)
	}
	if res.Headline != "CrashLoopBackOff" {
		t.Errorf("Headline = %q, want CrashLoopBackOff", res.Headline)
	}
}
