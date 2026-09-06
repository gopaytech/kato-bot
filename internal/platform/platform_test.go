package platform

import (
	"context"
	"testing"
)

type fakeAdapter struct{ name string }

func (f fakeAdapter) Name() string                  { return f.name }
func (f fakeAdapter) Start(_ context.Context) error { return nil }

func TestNonNilDropsNilAdapters(t *testing.T) {
	a := fakeAdapter{name: "lark"}
	b := fakeAdapter{name: "telegram"}

	got := NonNil(a, nil, b, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 adapters, got %d", len(got))
	}
	if got[0].Name() != "lark" || got[1].Name() != "telegram" {
		t.Fatalf("order/identity wrong: %q, %q", got[0].Name(), got[1].Name())
	}
}

func TestNonNilAllNil(t *testing.T) {
	if got := NonNil(nil, nil); len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}
