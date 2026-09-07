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
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))
	if _, err := Load(); err == nil {
		t.Fatal("expected error when neither Lark nor Telegram is configured")
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
    concurrency: 5
    usecases:
      - usecase: deployment-troubleshooting
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
	if g.Name != "critical-services" || g.Cluster != "prod-1" {
		t.Errorf("bad group header: %+v", g)
	}
	if g.Concurrency != 5 {
		t.Errorf("bad group knobs: %+v", g)
	}
	if len(g.UseCases) != 1 || g.UseCases[0].UseCase != "deployment-troubleshooting" {
		t.Errorf("bad usecases: %+v", g.UseCases)
	}
	if len(g.UseCases[0].Targets) != 2 || g.UseCases[0].Targets[0]["deployment"] != "payment-api" {
		t.Errorf("bad targets: %+v", g.UseCases[0].Targets)
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
		"dup name":      "groups:\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n",
		"empty usecase": "groups:\n  - {name: a, cluster: c, usecases: [{usecase: '', targets: [{x: y}]}]}\n",
		"no targets":    "groups:\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: []}]}\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadGroups(write(body)); err == nil {
				t.Errorf("expected validation error for %s", name)
			}
		})
	}
}

func TestLoadGroupsMultiUseCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.yaml")
	os.WriteFile(path, []byte(`
groups:
  - name: critical
    cluster: prod-1
    concurrency: 5
    usecases:
      - usecase: deployment-troubleshooting
        targets:
          - { namespace: payments, deployment: payment-api }
          - { namespace: cart, deployment: cart-api }
      - usecase: http-connectivity-check
        targets:
          - { target: api, port: "443", scheme: https, path: /healthz }
`), 0o600)
	groups, err := loadGroups(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Name != "critical" || g.Cluster != "prod-1" || g.Concurrency != 5 {
		t.Errorf("bad header: %+v", g)
	}
	if len(g.UseCases) != 2 || g.UseCases[0].UseCase != "deployment-troubleshooting" ||
		len(g.UseCases[0].Targets) != 2 || g.UseCases[1].UseCase != "http-connectivity-check" ||
		g.UseCases[1].Targets[0]["target"] != "api" {
		t.Errorf("bad usecases: %+v", g.UseCases)
	}
}

func TestLoadGroupsValidationMulti(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "g.yaml")
		os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	cases := map[string]string{
		"no usecases":   "groups:\n  - {name: a, cluster: c, usecases: []}\n",
		"empty usecase": "groups:\n  - name: a\n    cluster: c\n    usecases:\n      - {usecase: '', targets: [{x: y}]}\n",
		"no targets":    "groups:\n  - name: a\n    cluster: c\n    usecases:\n      - {usecase: u, targets: []}\n",
		"dup name":      "groups:\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n  - {name: a, cluster: c, usecases: [{usecase: u, targets: [{x: y}]}]}\n",
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
    concurrency: 5
    usecases:
      - usecase: deployment-troubleshooting
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

func TestLoadGroupSummary(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GROUP_SUMMARY_ENABLED", "true")
	t.Setenv("GROUP_SUMMARY_MODEL", "gpt-4o-mini")
	t.Setenv("GROUP_SUMMARY_API_KEY", "sk-test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.GroupSummary.Enabled {
		t.Error("GroupSummary.Enabled = false, want true")
	}
	if cfg.GroupSummary.Model != "gpt-4o-mini" {
		t.Errorf("GroupSummary.Model = %q, want gpt-4o-mini", cfg.GroupSummary.Model)
	}
	if cfg.GroupSummary.APIKey != "sk-test" {
		t.Errorf("GroupSummary.APIKey = %q, want sk-test", cfg.GroupSummary.APIKey)
	}
	if cfg.GroupSummary.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("GroupSummary.BaseURL = %q, want default", cfg.GroupSummary.BaseURL)
	}
	if cfg.GroupSummary.MaxTokens != 1024 {
		t.Errorf("GroupSummary.MaxTokens = %d, want default 1024", cfg.GroupSummary.MaxTokens)
	}
	if cfg.GroupSummary.Temperature != 0.2 {
		t.Errorf("GroupSummary.Temperature = %v, want default 0.2", cfg.GroupSummary.Temperature)
	}
	if cfg.GroupSummary.MaxEvidenceBytes != 16384 {
		t.Errorf("GroupSummary.MaxEvidenceBytes = %d, want default 16384", cfg.GroupSummary.MaxEvidenceBytes)
	}
}

func TestLoadGroupSummaryModelDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GroupSummary.Model != "gpt-4o-mini" {
		t.Errorf("GroupSummary.Model = %q, want default gpt-4o-mini", cfg.GroupSummary.Model)
	}
}

func TestLoadBadGroupSummaryEnabled(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("GROUP_SUMMARY_ENABLED", "not-a-bool")
	if _, err := Load(); err == nil {
		t.Fatal("expected error on bad GROUP_SUMMARY_ENABLED")
	}
}

const groupWithSummary = `groups:
  - name: critical-services
    cluster: prod-1
    summary: true
    usecases:
      - usecase: deployment-troubleshooting
        targets:
          - { namespace: payments, deployment: payment-api }
`

func TestLoadGroupsSummaryDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("KATO_GROUPS_FILE", writeGroups(t, groupWithSummary))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(cfg.Groups))
	}
	if !cfg.Groups[0].Summary {
		t.Error("Groups[0].Summary = false, want true")
	}
}

func TestLoadTelegramOnlyOK(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("telegram-only should load: %v", err)
	}
	if cfg.TelegramBotToken != "123:abc" {
		t.Fatalf("token not loaded: %q", cfg.TelegramBotToken)
	}
	if cfg.TelegramAPIBaseURL != "https://api.telegram.org" {
		t.Fatalf("default api base wrong: %q", cfg.TelegramAPIBaseURL)
	}
	if cfg.TelegramPollTimeout != 30*time.Second {
		t.Fatalf("default poll timeout wrong: %v", cfg.TelegramPollTimeout)
	}
}

func TestLoadTelegramPollTimeoutOverride(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_API_BASE_URL", "https://example.test")
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "5s")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.TelegramAPIBaseURL != "https://example.test" {
		t.Errorf("TelegramAPIBaseURL = %q, want override", cfg.TelegramAPIBaseURL)
	}
	if cfg.TelegramPollTimeout != 5*time.Second {
		t.Errorf("TelegramPollTimeout = %v, want 5s", cfg.TelegramPollTimeout)
	}
}

func TestLoadBadTelegramPollTimeout(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "notaduration")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	if _, err := Load(); err == nil {
		t.Fatal("expected error on bad TELEGRAM_POLL_TIMEOUT duration")
	}
}

func TestLoadNeitherPlatformFails(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	if _, err := Load(); err == nil {
		t.Fatal("expected error when neither platform is configured")
	}
}

func TestLoadPartialLarkFails(t *testing.T) {
	t.Setenv("LARK_APP_ID", "cli_x")
	t.Setenv("LARK_APP_SECRET", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	if _, err := Load(); err == nil {
		t.Fatal("expected error when Lark is half-configured and Telegram absent")
	}
}

func TestLoadPartialLarkOtherSideFails(t *testing.T) {
	t.Setenv("LARK_APP_ID", "")
	t.Setenv("LARK_APP_SECRET", "secret_x")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	if _, err := Load(); err == nil {
		t.Fatal("expected error when only LARK_APP_SECRET is set")
	}
}

func TestLoadTelegramAllowedChatsAndUsers(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_ALLOWED_CHATS", " -1001234567890, 123 ")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "42, 43")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantChats := []int64{-1001234567890, 123}
	if len(cfg.TelegramAllowedChats) != len(wantChats) {
		t.Fatalf("TelegramAllowedChats = %v, want %v", cfg.TelegramAllowedChats, wantChats)
	}
	for i, v := range wantChats {
		if cfg.TelegramAllowedChats[i] != v {
			t.Errorf("TelegramAllowedChats[%d] = %d, want %d", i, cfg.TelegramAllowedChats[i], v)
		}
	}
	wantUsers := []int64{42, 43}
	if len(cfg.TelegramAllowedUsers) != len(wantUsers) {
		t.Fatalf("TelegramAllowedUsers = %v, want %v", cfg.TelegramAllowedUsers, wantUsers)
	}
	for i, v := range wantUsers {
		if cfg.TelegramAllowedUsers[i] != v {
			t.Errorf("TelegramAllowedUsers[%d] = %d, want %d", i, cfg.TelegramAllowedUsers[i], v)
		}
	}
}

func TestLoadTelegramAllowedListsEmptyIsNil(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.TelegramAllowedChats != nil {
		t.Errorf("TelegramAllowedChats = %v, want nil", cfg.TelegramAllowedChats)
	}
	if cfg.TelegramAllowedUsers != nil {
		t.Errorf("TelegramAllowedUsers = %v, want nil", cfg.TelegramAllowedUsers)
	}
}

func TestLoadTelegramAllowedChatsBadValue(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_ALLOWED_CHATS", "1,abc")

	if _, err := Load(); err == nil {
		t.Fatal("expected error on malformed TELEGRAM_ALLOWED_CHATS")
	}
}

func TestLoadTelegramAllowedUsersBadValue(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "1,abc")

	if _, err := Load(); err == nil {
		t.Fatal("expected error on malformed TELEGRAM_ALLOWED_USERS")
	}
}

func TestParseInt64List(t *testing.T) {
	got, err := parseInt64List("TELEGRAM_ALLOWED_CHATS", " -1001234567890 , 123 ,, 456")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []int64{-1001234567890, 123, 456}
	if len(got) != len(want) {
		t.Fatalf("got = %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("got[%d] = %d, want %d", i, got[i], v)
		}
	}

	if got, err := parseInt64List("TELEGRAM_ALLOWED_CHATS", ""); err != nil || got != nil {
		t.Errorf("empty input: got = %v, err = %v, want nil, nil", got, err)
	}

	if _, err := parseInt64List("TELEGRAM_ALLOWED_CHATS", "1,foo"); err == nil {
		t.Error("expected error on malformed element")
	}
}

func TestLoadBothPlatformsOK(t *testing.T) {
	t.Setenv("LARK_APP_ID", "cli_x")
	t.Setenv("LARK_APP_SECRET", "secret_x")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("KATO_CLUSTERS_FILE", writeClusters(t, twoClusters))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("both platforms configured should load: %v", err)
	}
	if cfg.LarkAppID != "cli_x" || cfg.TelegramBotToken != "123:abc" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
