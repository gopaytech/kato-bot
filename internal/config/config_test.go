package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeClusters writes a clusters YAML file to a temp dir and returns its path.
func writeClusters(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clusters.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setRequiredEnv sets the minimum env needed for Load to succeed: LARK_APP_ID,
// LARK_APP_SECRET, and a KATO_CLUSTERS_FILE pointing at a valid one-cluster file.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LARK_APP_ID", "cli_x")
	t.Setenv("LARK_APP_SECRET", "secret_x")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))
}

const twoClusters = `clusters:
  - name: prod
    url: http://kato.prod.svc:8080
    label: Production
  - name: staging
    url: http://kato.staging.svc:8080
`

func TestLoadDefaults(t *testing.T) {
	t.Setenv("LARK_APP_ID", "cli_x")
	t.Setenv("LARK_APP_SECRET", "secret_x")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(cfg.Clusters) != 2 {
		t.Fatalf("clusters = %+v", cfg.Clusters)
	}
	if cfg.Clusters[0].Name != "prod" || cfg.Clusters[0].URL != "http://kato.prod.svc:8080" || cfg.Clusters[0].Label != "Production" {
		t.Errorf("cluster[0] = %+v", cfg.Clusters[0])
	}
	if cfg.Clusters[1].Name != "staging" || cfg.Clusters[1].Label != "" {
		t.Errorf("cluster[1] = %+v", cfg.Clusters[1])
	}
	if cfg.Clusters[0].InsecureSkipVerify || cfg.Clusters[1].InsecureSkipVerify {
		t.Errorf("insecureSkipVerify should default to false: %+v", cfg.Clusters)
	}
	if cfg.KatoRunTimeout != 360*time.Second {
		t.Errorf("timeout = %v", cfg.KatoRunTimeout)
	}
	if cfg.HealthAddr != ":8080" {
		t.Errorf("health = %q", cfg.HealthAddr)
	}
	if cfg.MaxConcurrentRuns != 4 {
		t.Errorf("maxConcurrentRuns = %d, want 4", cfg.MaxConcurrentRuns)
	}
	if cfg.LarkBaseURL != "https://open.larksuite.com" {
		t.Errorf("larkBaseURL = %q", cfg.LarkBaseURL)
	}
	if cfg.GroupRunTimeout != 1800*time.Second {
		t.Errorf("groupRunTimeout = %v, want 1800s", cfg.GroupRunTimeout)
	}
}

func TestLoadMaxConcurrentRuns(t *testing.T) {
	t.Setenv("LARK_APP_ID", "id")
	t.Setenv("LARK_APP_SECRET", "sec")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	t.Setenv("MAX_CONCURRENT_RUNS", "8")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.MaxConcurrentRuns != 8 {
		t.Errorf("maxConcurrentRuns = %d, want 8", cfg.MaxConcurrentRuns)
	}

	for _, bad := range []string{"0", "-1", "two"} {
		t.Setenv("MAX_CONCURRENT_RUNS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("MAX_CONCURRENT_RUNS=%q should error", bad)
		}
	}
}

func TestLoadInsecureSkipVerify(t *testing.T) {
	t.Setenv("LARK_APP_ID", "id")
	t.Setenv("LARK_APP_SECRET", "sec")
	const body = `clusters:
  - name: secure
    url: https://kato.secure.svc:8443
  - name: selfsigned
    url: https://kato.selfsigned.svc:8443
    insecureSkipVerify: true
`
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, body))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Clusters[0].InsecureSkipVerify {
		t.Errorf("cluster[0] insecureSkipVerify = true, want false (omitted)")
	}
	if !cfg.Clusters[1].InsecureSkipVerify {
		t.Errorf("cluster[1] insecureSkipVerify = false, want true")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))
	if _, err := Load(); err == nil {
		t.Fatal("expected error when LARK_APP_ID/SECRET unset")
	}
}

func TestLoadBadTimeout(t *testing.T) {
	t.Setenv("LARK_APP_ID", "id")
	t.Setenv("LARK_APP_SECRET", "sec")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))
	t.Setenv("KATO_RUN_TIMEOUT", "soon")
	if _, err := Load(); err == nil {
		t.Fatal("expected error on bad duration")
	}
}

func TestLoadGroupRunTimeoutOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GROUP_RUN_TIMEOUT", "60s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.GroupRunTimeout != 60*time.Second {
		t.Errorf("groupRunTimeout = %v, want 60s", cfg.GroupRunTimeout)
	}
}

func TestLoadBadGroupRunTimeout(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GROUP_RUN_TIMEOUT", "notaduration")
	if _, err := Load(); err == nil {
		t.Fatal("expected error on bad GROUP_RUN_TIMEOUT duration")
	}
}

func TestLoadClustersValidation(t *testing.T) {
	t.Setenv("LARK_APP_ID", "id")
	t.Setenv("LARK_APP_SECRET", "sec")

	cases := []struct {
		name string
		body string
	}{
		{"empty list", "clusters: []\n"},
		{"missing url", "clusters:\n  - name: prod\n"},
		{"empty name", "clusters:\n  - name: \"\"\n    url: http://x\n"},
		{"duplicate name", "clusters:\n  - name: prod\n    url: http://a\n  - name: prod\n    url: http://b\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, tc.body))
			if _, err := Load(); err == nil {
				t.Errorf("%s: expected a validation error", tc.name)
			}
		})
	}
}

func TestLoadClustersFileMissing(t *testing.T) {
	t.Setenv("LARK_APP_ID", "id")
	t.Setenv("LARK_APP_SECRET", "sec")
	t.Setenv("KATO_CLUSTERS_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if _, err := Load(); err == nil {
		t.Fatal("expected an error when the clusters file is missing")
	}
}

func TestLoadGroups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.yaml")
	os.WriteFile(path, []byte(`
groups:
  - name: critical-services
    cluster: prod-1
    usecase: deployment-troubleshooting
    concurrency: 5
    targets:
      - { namespace: payments, deployment: payment-api }
      - { namespace: cart, deployment: cart-api }
`), 0o600)

	groups, err := loadGroups(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Name != "critical-services" || g.Cluster != "prod-1" || g.UseCase != "deployment-troubleshooting" {
		t.Errorf("bad group header: %+v", g)
	}
	if g.Concurrency != 5 {
		t.Errorf("bad group knobs: %+v", g)
	}
	if len(g.Targets) != 2 || g.Targets[0]["deployment"] != "payment-api" {
		t.Errorf("bad targets: %+v", g.Targets)
	}
}

func TestLoadGroupsValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "g.yaml")
		os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	cases := map[string]string{
		"dup name":      "groups:\n  - {name: a, cluster: c, usecase: u, targets: [{x: y}]}\n  - {name: a, cluster: c, usecase: u, targets: [{x: y}]}\n",
		"empty usecase": "groups:\n  - {name: a, cluster: c, usecase: '', targets: [{x: y}]}\n",
		"no targets":    "groups:\n  - {name: a, cluster: c, usecase: u, targets: []}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadGroups(write(body)); err == nil {
				t.Errorf("expected validation error for %s", name)
			}
		})
	}
}

func TestLoadGroupsMissingFileIsEmpty(t *testing.T) {
	groups, err := loadGroups(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing groups file must be non-fatal, got %v", err)
	}
	if groups != nil {
		t.Errorf("groups = %+v, want nil", groups)
	}
}

// writeGroups writes a groups YAML file to a temp dir and returns its path.
func writeGroups(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "groups.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneGroup = `groups:
  - name: critical-services
    cluster: prod-1
    usecase: deployment-troubleshooting
    concurrency: 5
    targets:
      - { namespace: payments, deployment: payment-api }
`

func TestLoadGroupsViaLoad(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("KATO_GROUPS_FILE", writeGroups(t, oneGroup))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(cfg.Groups))
	}
	if cfg.Groups[0].Name != "critical-services" || cfg.Groups[0].Cluster != "prod-1" {
		t.Errorf("groups[0] = %+v", cfg.Groups[0])
	}
}

// API_ADDR: default :9090; explicitly empty disables the API listener.
func TestAPIAddr(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		os.Unsetenv("API_ADDR") // ensure not set: Go test env is process-global
		setRequiredEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIAddr != ":9090" {
			t.Errorf("APIAddr = %q, want :9090", cfg.APIAddr)
		}
	})
	t.Run("explicit empty disables", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("API_ADDR", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIAddr != "" {
			t.Errorf("APIAddr = %q, want empty (disabled)", cfg.APIAddr)
		}
	})
	t.Run("override", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("API_ADDR", ":7777")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.APIAddr != ":7777" {
			t.Errorf("APIAddr = %q, want :7777", cfg.APIAddr)
		}
	})
}
