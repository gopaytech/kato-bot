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

// GroupConfig is one predefined batch: a UseCase run across a static list of targets
// in one cluster.
type GroupConfig struct {
	Name        string
	Cluster     string
	UseCase     string
	Concurrency int
	Targets     []map[string]string
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
}

// Load reads config from env, applying defaults. LARK_APP_ID and LARK_APP_SECRET are
// required; the clusters file (KATO_CLUSTERS_FILE, default /etc/kato-bot/clusters.yaml)
// must exist, parse, and list at least one valid cluster. KATO_RUN_TIMEOUT must parse as a
// Go duration and MAX_CONCURRENT_RUNS as a positive int when set.
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
	if strings.TrimSpace(cfg.LarkAppID) == "" || strings.TrimSpace(cfg.LarkAppSecret) == "" {
		return Config{}, fmt.Errorf("LARK_APP_ID and LARK_APP_SECRET are required")
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
	return cfg, nil
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
		Name        string              `yaml:"name"`
		Cluster     string              `yaml:"cluster"`
		UseCase     string              `yaml:"usecase"`
		Concurrency int                 `yaml:"concurrency"`
		Targets     []map[string]string `yaml:"targets"`
	} `yaml:"groups"`
}

const defaultGroupConcurrency = 5

// loadGroups reads and validates the groups YAML file. A missing file is not an
// error (groups are optional) — it yields zero groups. Each group needs a unique
// non-empty name, a non-empty cluster and usecase, and at least one target.
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
		if strings.TrimSpace(g.UseCase) == "" {
			return nil, fmt.Errorf("groups file %s: group %q has an empty usecase", path, name)
		}
		if len(g.Targets) == 0 {
			return nil, fmt.Errorf("groups file %s: group %q has no targets", path, name)
		}
		conc := g.Concurrency
		if conc < 1 {
			conc = defaultGroupConcurrency
		}
		seen[name] = true
		out = append(out, GroupConfig{
			Name: name, Cluster: strings.TrimSpace(g.Cluster), UseCase: strings.TrimSpace(g.UseCase),
			Concurrency: conc, Targets: g.Targets,
		})
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
