package telegram

import (
	"sync"
	"time"
)

// pending is one in-flight input wizard.
type pending struct {
	user      int64
	cluster   string
	usecase   string
	collected map[string]string
	next      string // the input currently being prompted; "" when none
	deadline  time.Time
}

type msgKey struct {
	chat int64
	msg  int
}
type userKey struct {
	chat int64
	user int64
}

// sessions holds wizard state keyed by (chat, message) with a (chat, user)
// reverse index. Safe for concurrent use — go-telegram/bot dispatches updates
// on separate goroutines.
type sessions struct {
	mu  sync.Mutex
	byM map[msgKey]*pending
	byU map[userKey]msgKey
	ttl time.Duration
}

func newSessions(ttl time.Duration) *sessions {
	return &sessions{byM: map[msgKey]*pending{}, byU: map[userKey]msgKey{}, ttl: ttl}
}

func (s *sessions) begin(chat int64, msg int, user int64, cluster, usecase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Drop any previous flow for this user (one active wizard per user per chat).
	uk := userKey{chat, user}
	if old, ok := s.byU[uk]; ok {
		delete(s.byM, old)
	}
	mk := msgKey{chat, msg}
	s.byM[mk] = &pending{
		user: user, cluster: cluster, usecase: usecase,
		collected: map[string]string{}, deadline: time.Now().Add(s.ttl),
	}
	s.byU[uk] = mk
}

func (s *sessions) byMsg(chat int64, msg int) (*pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byM[msgKey{chat, msg}]
	return p, ok
}

func (s *sessions) byUser(chat, user int64) (int, *pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk, ok := s.byU[userKey{chat, user}]
	if !ok {
		return 0, nil, false
	}
	return mk.msg, s.byM[mk], true
}

func (s *sessions) update(chat int64, msg int, collected map[string]string, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.byM[msgKey{chat, msg}]; ok {
		cp := make(map[string]string, len(collected))
		for k, v := range collected {
			cp[k] = v
		}
		p.collected = cp
		p.next = next
		p.deadline = time.Now().Add(s.ttl)
	}
}

func (s *sessions) end(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk := msgKey{chat, msg}
	if p, ok := s.byM[mk]; ok {
		delete(s.byU, userKey{chat, p.user})
		delete(s.byM, mk)
	}
}

// replyContext returns a snapshot of the active wizard for (chat,user) under
// the lock: the message id, cluster, usecase, the input currently awaited, and
// a COPY of the collected inputs. ok is false when there is no session or no
// input is pending. The caller never touches the live *pending.
func (s *sessions) replyContext(chat, user int64) (msg int, cluster, usecase, next string, collected map[string]string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk, found := s.byU[userKey{chat, user}]
	if !found {
		return 0, "", "", "", nil, false
	}
	p := s.byM[mk]
	if p == nil || p.next == "" {
		return 0, "", "", "", nil, false
	}
	cp := make(map[string]string, len(p.collected))
	for k, v := range p.collected {
		cp[k] = v
	}
	return mk.msg, p.cluster, p.usecase, p.next, cp, true
}

func (s *sessions) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for mk, p := range s.byM {
		if now.After(p.deadline) {
			delete(s.byU, userKey{mk.chat, p.user})
			delete(s.byM, mk)
		}
	}
}
