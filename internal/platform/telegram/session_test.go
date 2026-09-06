package telegram

import (
	"testing"
	"time"
)

func TestSessionBeginFindEnd(t *testing.T) {
	s := newSessions(10 * time.Minute)
	s.begin(100, 5, 42, "prod", "deploy-check")

	p, ok := s.byMsg(100, 5)
	if !ok || p.cluster != "prod" || p.usecase != "deploy-check" || p.user != 42 {
		t.Fatalf("byMsg wrong: %#v ok=%v", p, ok)
	}

	msg, p2, ok := s.byUser(100, 42)
	if !ok || msg != 5 || p2 != p {
		t.Fatalf("byUser wrong: msg=%d ok=%v", msg, ok)
	}

	s.update(100, 5, map[string]string{"namespace": "payments"}, "deployment")
	p, _ = s.byMsg(100, 5)
	if p.collected["namespace"] != "payments" || p.next != "deployment" {
		t.Fatalf("update not applied: %#v", p)
	}

	s.end(100, 5)
	if _, ok := s.byMsg(100, 5); ok {
		t.Fatal("byMsg after end should miss")
	}
	if _, _, ok := s.byUser(100, 42); ok {
		t.Fatal("byUser after end should miss (reverse index cleaned)")
	}
}

func TestSessionSweepEvictsExpired(t *testing.T) {
	s := newSessions(1 * time.Minute)
	s.begin(1, 1, 1, "c", "u")
	s.sweep(time.Now().Add(2 * time.Minute))
	if _, ok := s.byMsg(1, 1); ok {
		t.Fatal("expired session should be swept")
	}
	if _, _, ok := s.byUser(1, 1); ok {
		t.Fatal("reverse index should be swept too")
	}
}

func TestSessionOnePerUserReplacesOld(t *testing.T) {
	s := newSessions(10 * time.Minute)
	s.begin(1, 10, 7, "c", "u1")
	s.begin(1, 20, 7, "c", "u2") // same user starts a new flow
	if _, ok := s.byMsg(1, 10); ok {
		t.Fatal("old session for the user should be dropped")
	}
	msg, p, ok := s.byUser(1, 7)
	if !ok || msg != 20 || p.usecase != "u2" {
		t.Fatalf("reverse index should point to newest: msg=%d %#v", msg, p)
	}
}
