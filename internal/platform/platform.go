// Package platform holds the generic lifecycle seam through which cmd/kato-bot
// runs any chat platform (Lark, Telegram) uniformly. It is platform-neutral: it
// imports core and summary, and neither imports it back.
package platform

import (
	"context"
	"time"

	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/summary"
)

// Adapter is one running chat platform. main.go constructs the enabled ones and
// runs each Start in its own goroutine. Start blocks until ctx is cancelled.
type Adapter interface {
	Name() string
	Start(ctx context.Context) error
}

// Deps bundles the shared singletons every platform is wired from. One set is
// built in main and passed to each platform's New.
type Deps struct {
	Clusters                *core.Registry
	Groups                  *core.GroupRegistry
	Runner                  *core.GroupRunner
	Summarizer              summary.Client // nil disables the LLM group summary
	RunTimeout              time.Duration
	GroupTimeout            time.Duration
	MaxConcurrent           int
	LogLevel                string
	SummaryMaxEvidenceBytes int
}

// NonNil returns the non-nil adapters in order. A platform's New returns a nil
// Adapter when it is not configured; NonNil drops those.
func NonNil(as ...Adapter) []Adapter {
	out := make([]Adapter, 0, len(as))
	for _, a := range as {
		if a != nil {
			out = append(out, a)
		}
	}
	return out
}
