package lark

import (
	"strings"

	"github.com/gopaytech/kato-bot/internal/config"
	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/platform"
)

// New builds the Lark adapter from config + shared deps, or returns (nil, nil)
// when Lark is not configured (no app id/secret). It encapsulates the renderer +
// its own core.Core so main.go holds no Lark-specific wiring.
func New(cfg config.Config, d platform.Deps) (platform.Adapter, error) {
	if strings.TrimSpace(cfg.LarkAppID) == "" || strings.TrimSpace(cfg.LarkAppSecret) == "" {
		return nil, nil
	}
	renderer := NewSender(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkBaseURL)
	c := &core.Core{Clusters: d.Clusters, Groups: d.Groups, R: renderer}
	return &Adapter{
		AppID:         cfg.LarkAppID,
		AppSecret:     cfg.LarkAppSecret,
		Core:          c,
		R:             renderer,
		RunTimeout:    d.RunTimeout,
		LogLevel:      d.LogLevel,
		MaxConcurrent: d.MaxConcurrent,
		BaseURL:       cfg.LarkBaseURL,

		Groups:       d.Groups,
		GroupRunner:  d.Runner,
		GroupTimeout: d.GroupTimeout,

		Summarizer:              d.Summarizer,
		SummaryMaxEvidenceBytes: d.SummaryMaxEvidenceBytes,
	}, nil
}

var _ platform.Adapter = (*Adapter)(nil)
