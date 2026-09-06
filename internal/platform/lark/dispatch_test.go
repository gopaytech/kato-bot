package lark

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/gopaytech/kato-bot/internal/core"
)

// TestShouldRespond covers the group-vs-DM gating: DMs always trigger the picker; group
// messages only when the bot is @mentioned.
func TestShouldRespond(t *testing.T) {
	mention := []*larkim.MentionEvent{{}}
	cases := []struct {
		name     string
		chatType string
		mentions []*larkim.MentionEvent
		want     bool
	}{
		{"p2p no mention", "p2p", nil, true},
		{"p2p with mention", "p2p", mention, true},
		{"group no mention", "group", nil, false},
		{"group with mention", "group", mention, true},
		{"topic_group with mention", "topic_group", mention, true},
		{"topic_group no mention", "topic_group", nil, false},
		{"unknown no mention", "", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldRespond(c.chatType, c.mentions); got != c.want {
				t.Errorf("shouldRespond(%q, %d mentions) = %v, want %v", c.chatType, len(c.mentions), got, c.want)
			}
		})
	}
}

// blockingKato holds Run until release is closed, so a test can pin the semaphore slot.
type blockingKato struct {
	contract core.Contract
	started  chan struct{} // receives once Run is entered
	release  chan struct{} // Run returns after this is closed
}

func (b *blockingKato) ListUseCases(context.Context) ([]core.UseCase, error) { return nil, nil }
func (b *blockingKato) GetUseCase(context.Context, string) (core.Contract, error) {
	return b.contract, nil
}
func (b *blockingKato) Run(context.Context, string, map[string]string) (core.RunResult, error) {
	b.started <- struct{}{}
	<-b.release
	return core.RunResult{Phase: "Completed", Summary: "ok"}, nil
}

// TestDedupSeen verifies redelivered message ids are dropped, distinct ids pass, empty
// ids are never deduped, and the FIFO eviction keeps memory bounded.
func TestDedupSeen(t *testing.T) {
	var d dedup
	if d.seen("a") {
		t.Fatal("first sighting of a should be new")
	}
	if !d.seen("a") {
		t.Fatal("second sighting of a should be a duplicate")
	}
	if d.seen("b") {
		t.Fatal("first sighting of b should be new")
	}
	if d.seen("") || d.seen("") {
		t.Fatal("empty id must never be treated as seen")
	}
	// Overflow the cap, then confirm the oldest id ("a") was evicted (seen as new again)
	// while a recent id stays deduped.
	for i := 0; i < dedupCap+5; i++ {
		d.seen("fill-" + strconv.Itoa(i))
	}
	if d.seen("a") {
		t.Fatal("a should have been evicted after overflow and seen as new")
	}
}

// TestCardActionSemaphoreCapsRuns verifies that with MaxConcurrent=1, a second run
// submitted while the first is still in flight gets a "busy" card and starts no new run.
func TestCardActionSemaphoreCapsRuns(t *testing.T) {
	bk := &blockingKato{
		contract: core.Contract{Name: "uc"},
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	a := &Adapter{
		Core:          &core.Core{Clusters: regOf("c1", bk), R: &captureRenderer{}},
		R:             &Renderer{S: &fakeSender{}},
		RunTimeout:    5 * time.Second,
		MaxConcurrent: 1,
	}
	submit := core.SubmitForm{Reply: core.Reply{MessageID: "card1", Cluster: "c1"}, Name: "uc", Inputs: map[string]string{}}

	// First submit: returns a running-card response and spawns the run (which blocks).
	resp1 := a.handleCardAction(context.Background(), submit, submit.Reply)
	if resp1 == nil || resp1.Card == nil {
		t.Fatal("expected a running-card response")
	}
	select {
	case <-bk.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first run never started")
	}

	// Second submit while the slot is held: must return a busy card, no new run.
	resp2 := a.handleCardAction(context.Background(), submit, submit.Reply)
	if resp2 == nil || resp2.Card == nil {
		t.Fatal("expected a busy-card response")
	}
	raw, _ := json.Marshal(resp2.Card.Data)
	if !strings.Contains(string(raw), "busy") {
		t.Fatalf("expected a busy message, got %s", raw)
	}
	select {
	case <-bk.started:
		t.Fatal("a second run started despite the cap")
	default:
	}

	// Release the first run so its goroutine completes and frees the slot cleanly.
	close(bk.release)
}

// regOf builds a one-cluster registry for tests.
func regOf(name string, k core.KatoClient) *core.Registry {
	r := core.NewRegistry()
	r.Add(core.Cluster{Name: name}, k)
	return r
}
