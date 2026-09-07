// Package config loads kato-bot configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ClusterConfig is one configured kato backend (name → URL, with an optional label).
type ClusterConfig struct {
	Name  string
	URL   string
	Label string
	// InsecureSkipVerify disables TLS certificate verification for this cluster's kato
	// URL. Only meaningful for https URLs; use for self-signed certs on a trusted network.
	InsecureSkipVerify bool
}

// GroupConfig is one predefined batch: several UseCases, each run across its own
// list of targets, in one cluster.
type GroupConfig struct {
	Name        string
	Cluster     string
	Concurrency int
	UseCases    []GroupUseCase
	// Summary is the per-group default for whether a group run also produces an
	// LLM summary (used when the caller doesn't explicitly request one).
	Summary bool
}

// GroupUseCase is one UseCase within a group and the targets it runs on.
type GroupUseCase struct {
	UseCase string
	Targets []map[string]string
}

// GroupSummaryConfig configures kato-bot's optional LLM group summarizer.
type GroupSummaryConfig struct {
	Enabled          bool
	BaseURL          string
	Model            string
	APIKey           string
	MaxTokens        int
	Temperature      float64
	Timeout          time.Duration
	MaxEvidenceBytes int
}

// Config is the resolved runtime configuration.
type Config struct {
	LarkAppID         string
	LarkAppSecret     string
	Clusters          []ClusterConfig
	Groups            []GroupConfig
	KatoRunTimeout    time.Duration
	GroupRunTimeout   time.Duration
	HealthAddr        string
	LogLevel          string
	MaxConcurrentRuns int
	LarkBaseURL       string
	// APIAddr is the MCP + REST proxy listen address; empty disables the listener.
	APIAddr string
	// GroupSummary configures the optional LLM group summarizer.
	GroupSummary GroupSummaryConfig
	// Telegram bot: presence of TelegramBotToken enables the Telegram adapter.
	TelegramBotToken    string
	TelegramAPIBaseURL  string
	TelegramPollTimeout time.Duration
	// TelegramAllowedChats / TelegramAllowedUsers optionally restrict which Telegram
	// chats/users may use the bot. Empty (nil) means unrestricted for that dimension —
	// both empty (the default) preserves the historical open-to-anyone behavior.
	TelegramAllowedChats []int64
	TelegramAllowedUsers []int64
}

// Load reads config from env, applying defaults. At least one of the two supported
// platforms must be configured: Lark (both LARK_APP_ID and LARK_APP_SECRET set) and/or
// Telegram (TELEGRAM_BOT_TOKEN set); setting only one of LARK_APP_ID/LARK_APP_SECRET is an
// error. The clusters file (KATO_CLUSTERS_FILE, default /etc/kato-bot/clusters.yaml) must
// exist, parse, and list at least one valid cluster. KATO_RUN_TIMEOUT must parse as a Go
// duration and MAX_CONCURRENT_RUNS as a positive int when set.
func Load() (Config, error) {
	cfg := Config{
		LarkAppID:         os.Getenv("LARK_APP_ID"),
		LarkAppSecret:     os.Getenv("LARK_APP_SECRET"),
		HealthAddr:        envOr("HEALTH_ADDR", ":8080"),
		LogLevel:          envOr("LOG_LEVEL", "info"),
		KatoRunTimeout:    360 * time.Second,
		GroupRunTimeout:   1800 * time.Second,
		MaxConcurrentRuns: 4,
		// Open-platform base URL. Lark international: https://open.larksuite.com;
		// Feishu (China): https://open.feishu.cn.
		LarkBaseURL: envOr("LARK_BASE_URL", "https://open.larksuite.com"),
	}

	clusters, err := loadClusters(envOr("KATO_CLUSTERS_FILE", "/etc/kato-bot/clusters.yaml"))
	if err != nil {
		return Config{}, err
	}
	cfg.Clusters = clusters

	groups, err := loadGroups(envOr("KATO_GROUPS_FILE", "/etc/kato-bot-groups/groups.yaml"))
	if err != nil {
		return Config{}, err
	}
	cfg.Groups = groups

	cfg.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	cfg.TelegramAPIBaseURL = envOr("TELEGRAM_API_BASE_URL", "https://api.telegram.org")
	cfg.TelegramPollTimeout = 30 * time.Second
	if v := os.Getenv("TELEGRAM_POLL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("TELEGRAM_POLL_TIMEOUT: %w", err)
		}
		cfg.TelegramPollTimeout = d
	}

	cfg.TelegramAllowedChats, err = parseInt64List("TELEGRAM_ALLOWED_CHATS", os.Getenv("TELEGRAM_ALLOWED_CHATS"))
	if err != nil {
		return Config{}, err
	}
	cfg.TelegramAllowedUsers, err = parseInt64List("TELEGRAM_ALLOWED_USERS", os.Getenv("TELEGRAM_ALLOWED_USERS"))
	if err != nil {
		return Config{}, err
	}

	larkID, larkSecret := strings.TrimSpace(cfg.LarkAppID), strings.TrimSpace(cfg.LarkAppSecret)
	larkEnabled := larkID != "" && larkSecret != ""
	larkPartial := (larkID != "") != (larkSecret != "")
	telegramEnabled := strings.TrimSpace(cfg.TelegramBotToken) != ""
	if larkPartial {
		return Config{}, fmt.Errorf("LARK_APP_ID and LARK_APP_SECRET must be set together")
	}
	if !larkEnabled && !telegramEnabled {
		return Config{}, fmt.Errorf("configure at least one platform: set LARK_APP_ID+LARK_APP_SECRET and/or TELEGRAM_BOT_TOKEN")
	}

	if v := os.Getenv("KATO_RUN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("KATO_RUN_TIMEOUT: %w", err)
		}
		cfg.KatoRunTimeout = d
	}
	if v := os.Getenv("GROUP_RUN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("GROUP_RUN_TIMEOUT: %w", err)
		}
		cfg.GroupRunTimeout = d
	}
	if v := os.Getenv("MAX_CONCURRENT_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("MAX_CONCURRENT_RUNS: must be a positive integer, got %q", v)
		}
		cfg.MaxConcurrentRuns = n
	}

	// API_ADDR: default :9090; an explicitly empty value disables the listener
	// (envOr cannot express that, hence LookupEnv).
	if v, ok := os.LookupEnv("API_ADDR"); ok {
		cfg.APIAddr = v
	} else {
		cfg.APIAddr = ":9090"
	}

	cfg.GroupSummary, err = loadGroupSummary()
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadGroupSummary reads the optional GROUP_SUMMARY_* env vars configuring the
// LLM group summarizer. Everything is optional; GROUP_SUMMARY_ENABLED defaults
// to false.
func loadGroupSummary() (GroupSummaryConfig, error) {
	gs := GroupSummaryConfig{
		BaseURL:          envOr("GROUP_SUMMARY_BASE_URL", "https://api.openai.com/v1"),
		Model:            envOr("GROUP_SUMMARY_MODEL", "gpt-4o-mini"),
		APIKey:           os.Getenv("GROUP_SUMMARY_API_KEY"),
		MaxTokens:        1024,
		Temperature:      0.2,
		MaxEvidenceBytes: 16384,
	}
	if v, ok := os.LookupEnv("GROUP_SUMMARY_ENABLED"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return GroupSummaryConfig{}, fmt.Errorf("GROUP_SUMMARY_ENABLED: %w", err)
		}
		gs.Enabled = b
	}
	if v := os.Getenv("GROUP_SUMMARY_MAX_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return GroupSummaryConfig{}, fmt.Errorf("GROUP_SUMMARY_MAX_TOKENS: %w", err)
		}
		gs.MaxTokens = n
	}
	if v := os.Getenv("GROUP_SUMMARY_TEMPERATURE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return GroupSummaryConfig{}, fmt.Errorf("GROUP_SUMMARY_TEMPERATURE: %w", err)
		}
		gs.Temperature = f
	}
	if v := os.Getenv("GROUP_SUMMARY_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return GroupSummaryConfig{}, fmt.Errorf("GROUP_SUMMARY_TIMEOUT: %w", err)
		}
		gs.Timeout = d
	}
	if v := os.Getenv("GROUP_SUMMARY_MAX_EVIDENCE_BYTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return GroupSummaryConfig{}, fmt.Errorf("GROUP_SUMMARY_MAX_EVIDENCE_BYTES: %w", err)
		}
		gs.MaxEvidenceBytes = n
	}
	return gs, nil
}

// clustersFile mirrors the YAML shape of the clusters config file.
type clustersFile struct {
	Clusters []struct {
		Name               string `yaml:"name"`
		URL                string `yaml:"url"`
		Label              string `yaml:"label"`
		InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
	} `yaml:"clusters"`
}

// loadClusters reads and validates the clusters YAML file: it must contain at least one
// cluster, each with a unique non-empty name and a non-empty url.
func loadClusters(path string) ([]ClusterConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clusters file %s: %w", path, err)
	}
	var f clustersFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse clusters file %s: %w", path, err)
	}
	if len(f.Clusters) == 0 {
		return nil, fmt.Errorf("clusters file %s: at least one cluster is required", path)
	}
	seen := make(map[string]bool, len(f.Clusters))
	out := make([]ClusterConfig, 0, len(f.Clusters))
	for i, c := range f.Clusters {
		name := strings.TrimSpace(c.Name)
		url := strings.TrimSpace(c.URL)
		if name == "" {
			return nil, fmt.Errorf("clusters file %s: cluster #%d has an empty name", path, i+1)
		}
		if url == "" {
			return nil, fmt.Errorf("clusters file %s: cluster %q has an empty url", path, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("clusters file %s: duplicate cluster name %q", path, name)
		}
		seen[name] = true
		out = append(out, ClusterConfig{
			Name:               name,
			URL:                url,
			Label:              strings.TrimSpace(c.Label),
			InsecureSkipVerify: c.InsecureSkipVerify,
		})
	}
	return out, nil
}

// groupsFile mirrors the YAML shape of the groups config file.
type groupsFile struct {
	Groups []struct {
		Name        string `yaml:"name"`
		Cluster     string `yaml:"cluster"`
		Concurrency int    `yaml:"concurrency"`
		UseCases    []struct {
			UseCase string              `yaml:"usecase"`
			Targets []map[string]string `yaml:"targets"`
		} `yaml:"usecases"`
		Summary bool `yaml:"summary"`
	} `yaml:"groups"`
}

const defaultGroupConcurrency = 5

// loadGroups reads and validates the groups YAML file. A missing file is not an
// error (groups are optional) — it yields zero groups. Each group needs a unique
// non-empty name, a non-empty cluster, at least one usecase, and each usecase
// needs a non-empty name and at least one target.
func loadGroups(path string) ([]GroupConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read groups file %s: %w", path, err)
	}
	var f groupsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse groups file %s: %w", path, err)
	}
	seen := make(map[string]bool, len(f.Groups))
	out := make([]GroupConfig, 0, len(f.Groups))
	for i, g := range f.Groups {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return nil, fmt.Errorf("groups file %s: group #%d has an empty name", path, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("groups file %s: duplicate group name %q", path, name)
		}
		if strings.TrimSpace(g.Cluster) == "" {
			return nil, fmt.Errorf("groups file %s: group %q has an empty cluster", path, name)
		}
		if len(g.UseCases) == 0 {
			return nil, fmt.Errorf("groups file %s: group %q has no usecases", path, name)
		}
		ucs := make([]GroupUseCase, 0, len(g.UseCases))
		for j, uc := range g.UseCases {
			un := strings.TrimSpace(uc.UseCase)
			if un == "" {
				return nil, fmt.Errorf("groups file %s: group %q usecase #%d has an empty usecase", path, name, j+1)
			}
			if len(uc.Targets) == 0 {
				return nil, fmt.Errorf("groups file %s: group %q usecase %q has no targets", path, name, un)
			}
			ucs = append(ucs, GroupUseCase{UseCase: un, Targets: uc.Targets})
		}
		conc := g.Concurrency
		if conc < 1 {
			conc = defaultGroupConcurrency
		}
		seen[name] = true
		out = append(out, GroupConfig{Name: name, Cluster: strings.TrimSpace(g.Cluster), Concurrency: conc, UseCases: ucs, Summary: g.Summary})
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseInt64List parses a comma-separated list of int64 (e.g. Telegram chat/user ids,
// which can be negative for groups). Each element is trimmed of surrounding spaces;
// empty elements (from "", "1,,2", leading/trailing commas) are skipped. An empty or
// unset s yields a nil list. A malformed element is reported as a config error
// prefixed with name, e.g. `TELEGRAM_ALLOWED_CHATS: invalid id "foo": ...`.
func parseInt64List(name, s string) ([]int64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid id %q: %w", name, p, err)
		}
		out = append(out, n)
	}
	return out, nil
}
