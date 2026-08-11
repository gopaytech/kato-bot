// internal/core/group_test.go
package core

import (
	"context"
	"testing"
)

func TestServiceResultBucket(t *testing.T) {
	tru, fls := true, false
	tests := []struct {
		name string
		r    ServiceResult
		want string
	}{
		{"errored", ServiceResult{Err: &RunError{Msg: "boom"}}, "errored"},
		{"unknown", ServiceResult{}, "unknown"},
		{"healthy", ServiceResult{Healthy: &tru}, "healthy"},
		{"unhealthy", ServiceResult{Healthy: &fls}, "unhealthy"},
		{"errored beats verdict", ServiceResult{Healthy: &tru, Err: &RunError{Msg: "x"}}, "errored"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Bucket(); got != tt.want {
				t.Errorf("Bucket() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGroupWorkItemsFromItems(t *testing.T) {
	g := Group{Items: []WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
	}}
	wi := g.WorkItems()
	if len(wi) != 2 || wi[0].UseCase != "dt" || wi[1].UseCase != "http" {
		t.Fatalf("WorkItems from Items = %+v", wi)
	}
}

func TestGroupUseCaseCounts(t *testing.T) {
	g := Group{Items: []WorkItem{
		{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}},
		{UseCase: "http", Inputs: map[string]string{"target": "x"}},
		{UseCase: "dt", Inputs: map[string]string{"deployment": "b"}},
	}}
	got := g.UseCaseCounts()
	if len(got) != 2 || got[0].UseCase != "dt" || got[0].Targets != 2 || got[1].UseCase != "http" || got[1].Targets != 1 {
		t.Fatalf("UseCaseCounts = %+v (want dt:2, http:1 in order)", got)
	}
}

func TestGroupRegistryForCluster(t *testing.T) {
	reg := NewGroupRegistry()
	reg.Add(Group{Name: "a", Cluster: "prod-1"})
	reg.Add(Group{Name: "b", Cluster: "prod-2"})
	reg.Add(Group{Name: "c", Cluster: "prod-1"})
	got := reg.ForCluster("prod-1")
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("ForCluster(prod-1) = %+v, want a,c in order", got)
	}
	if g, ok := reg.Get("b"); !ok || g.Cluster != "prod-2" {
		t.Errorf("Get(b) = %+v, %v", g, ok)
	}
}

// TestCollectingReporter proves CollectingReporter accumulates per-service
// results and tallies without rendering anything: Start sets Total, ServiceDone
// appends results and buckets tallies, Finish stores the final summary.
func TestCollectingReporter(t *testing.T) {
	ctx := context.Background()
	g := Group{Name: "critical", Cluster: "prod-1", Items: []WorkItem{{UseCase: "dt", Inputs: map[string]string{"deployment": "a"}}}}
	c := &CollectingReporter{}

	if err := c.Start(ctx, g, GroupDest{}, 3); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.Summary.Total != 3 || c.Summary.Group.Name != "critical" {
		t.Errorf("Start summary = %+v, want Total=3 Group=critical", c.Summary)
	}

	tru, fls := true, false
	if err := c.ServiceDone(ctx, g, ServiceResult{Target: map[string]string{"deployment": "a"}, Healthy: &tru}); err != nil {
		t.Fatalf("ServiceDone: %v", err)
	}
	if err := c.ServiceDone(ctx, g, ServiceResult{Target: map[string]string{"deployment": "b"}, Healthy: &fls}); err != nil {
		t.Fatalf("ServiceDone: %v", err)
	}
	if err := c.ServiceDone(ctx, g, ServiceResult{Target: map[string]string{"deployment": "c"}, Err: &RunError{Msg: "boom"}}); err != nil {
		t.Fatalf("ServiceDone: %v", err)
	}
	if len(c.Results) != 3 {
		t.Fatalf("Results = %d, want 3", len(c.Results))
	}
	if c.Summary.Healthy != 1 || c.Summary.Unhealthy != 1 || c.Summary.Errored != 1 || c.Summary.Unknown != 0 {
		t.Errorf("tallies after ServiceDone = %+v, want H1 U1 E1 Unk0", c.Summary)
	}

	final := GroupSummary{Group: g, Total: 3, Healthy: 1, Unhealthy: 1, Errored: 1}
	if err := c.Finish(ctx, final); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if c.Summary.Total != final.Total || c.Summary.Healthy != final.Healthy ||
		c.Summary.Unhealthy != final.Unhealthy || c.Summary.Errored != final.Errored ||
		c.Summary.Unknown != final.Unknown || c.Summary.Group.Name != final.Group.Name {
		t.Errorf("Finish summary = %+v, want %+v", c.Summary, final)
	}
}
