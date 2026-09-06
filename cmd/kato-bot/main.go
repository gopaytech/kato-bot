// Command kato-bot runs the Lark chat adapter for kato.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gopaytech/kato-bot/internal/api"
	"github.com/gopaytech/kato-bot/internal/config"
	"github.com/gopaytech/kato-bot/internal/core"
	"github.com/gopaytech/kato-bot/internal/gateway"
	"github.com/gopaytech/kato-bot/internal/groupapi"
	"github.com/gopaytech/kato-bot/internal/kato"
	mcpserver "github.com/gopaytech/kato-bot/internal/mcp"
	"github.com/gopaytech/kato-bot/internal/platform/lark"
	"github.com/gopaytech/kato-bot/internal/summary"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	renderer := lark.NewSender(cfg.LarkAppID, cfg.LarkAppSecret, cfg.LarkBaseURL)

	registry := core.NewRegistry()
	gw := gateway.New()
	names := make([]string, 0, len(cfg.Clusters))
	for _, cl := range cfg.Clusters {
		kc := kato.New(cl.URL, cfg.KatoRunTimeout, cl.InsecureSkipVerify)
		c := core.Cluster{Name: cl.Name, Label: cl.Label}
		registry.Add(c, kc)
		gw.Add(c, kc)
		names = append(names, cl.Name)
	}
	// Predefined groups: one registry shared by Core (interactive picker/run
	// flow) and the Adapter (kick-off + threaded reporting), so there is a
	// single source of truth for group definitions.
	groupReg := core.NewGroupRegistry()
	for _, gc := range cfg.Groups {
		if _, ok := registry.Get(gc.Cluster); !ok {
			log.Fatalf("group %q references unknown cluster %q", gc.Name, gc.Cluster)
		}
		var items []core.WorkItem
		for _, uc := range gc.UseCases {
			for _, t := range uc.Targets {
				items = append(items, core.WorkItem{UseCase: uc.UseCase, Inputs: t})
			}
		}
		groupReg.Add(core.Group{
			Name: gc.Name, Cluster: gc.Cluster, Concurrency: gc.Concurrency, Items: items, Summary: gc.Summary,
		})
	}
	groupRunner := &core.GroupRunner{Clusters: registry, MaxRetries: 3}

	c := &core.Core{Clusters: registry, Groups: groupReg, R: renderer}

	// Optional LLM group summarizer, shared by the interactive Lark adapter and
	// the REST/MCP groupapi.Service. A nil summarizer is total: both callers
	// degrade to a warning instead of failing.
	var summarizer summary.Client
	if cfg.GroupSummary.Enabled {
		summarizer = &summary.OpenAIClient{
			BaseURL: cfg.GroupSummary.BaseURL, Model: cfg.GroupSummary.Model,
			APIKey: cfg.GroupSummary.APIKey, MaxTokens: cfg.GroupSummary.MaxTokens,
			Temperature: cfg.GroupSummary.Temperature, Timeout: cfg.GroupSummary.Timeout,
		}
	}

	adapter := &lark.Adapter{
		AppID:         cfg.LarkAppID,
		AppSecret:     cfg.LarkAppSecret,
		Core:          c,
		R:             renderer,
		RunTimeout:    cfg.KatoRunTimeout,
		LogLevel:      cfg.LogLevel,
		MaxConcurrent: cfg.MaxConcurrentRuns,
		BaseURL:       cfg.LarkBaseURL,

		Groups:       groupReg,
		GroupRunner:  groupRunner,
		GroupTimeout: cfg.GroupRunTimeout,

		Summarizer:              summarizer,
		SummaryMaxEvidenceBytes: cfg.GroupSummary.MaxEvidenceBytes,
	}

	gapi := groupapi.New(groupReg, groupRunner, cfg.GroupRunTimeout)
	gapi.Summarizer = summarizer
	gapi.SummaryMaxEvidenceBytes = cfg.GroupSummary.MaxEvidenceBytes

	// Health server for k8s probes (no inbound app traffic; this is liveness only).
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
		log.Printf("health server on %s", cfg.HealthAddr)
		// A bind failure (e.g. HEALTH_ADDR clashes with a local port-forward) is fatal:
		// otherwise the bot runs but k8s probes fail, killing the pod with no clear cause.
		if err := http.ListenAndServe(cfg.HealthAddr, mux); err != nil {
			log.Fatalf("health server on %s: %v", cfg.HealthAddr, err)
		}
	}()

	// MCP + REST proxy listener (API_ADDR; empty = disabled). Serves the MCP
	// streamable-HTTP endpoint at /mcp and the cluster-prefixed REST proxy.
	if cfg.APIAddr != "" {
		apiMux := http.NewServeMux()
		apiMux.Handle("/mcp", mcpserver.Handler(mcpserver.NewServer(gw, gapi)))
		api.Register(apiMux, gw, gapi)
		go func() {
			log.Printf("api server (mcp + rest proxy) on %s", cfg.APIAddr)
			if err := http.ListenAndServe(cfg.APIAddr, apiMux); err != nil {
				log.Fatalf("api server on %s: %v", cfg.APIAddr, err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("kato-bot connecting to Lark; clusters=[%s] (run timeout %s, domain %s)",
		strings.Join(names, ", "), cfg.KatoRunTimeout, cfg.LarkBaseURL)
	if err := adapter.Start(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("lark adapter: %v", err)
	}
	log.Print("kato-bot shut down")
}
