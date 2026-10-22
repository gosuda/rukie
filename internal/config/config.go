package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type LogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
	Source bool   `toml:"source"`
}

type HTTPConfig struct {
	ExternalURL         string   `toml:"external_url"`
	Listen              []string `toml:"listen"`
	TrustedProxies      []string `toml:"trusted_proxies"`
	ReadTimeoutMs       int64    `toml:"read_timeout_ms"`
	ReadHeaderTimeoutMs int64    `toml:"read_header_timeout_ms"`
	WriteTimeoutMs      int64    `toml:"write_timeout_ms"`
	IdleTimeoutMs       int64    `toml:"idle_timeout_ms"`
	MaxRequestBodyBytes int64    `toml:"max_request_body_bytes"`
	ShutdownDrainMs     int64    `toml:"shutdown_drain_ms"`
	BehindProxy         bool     `toml:"behind_proxy"`
}

type SSHConfig struct {
	Shell         string   `toml:"shell"`
	Listen        []string `toml:"listen"`
	HostKeyFiles  []string `toml:"host_key_files"`
	MaxSessions   int      `toml:"max_sessions"`
	AuthTimeoutMs uint     `toml:"auth_timeout_ms"`
	IdleTimeoutMs int64    `toml:"idle_timeout_ms"`
	Enabled       bool     `toml:"enabled"`
}

type SQLiteConfig struct {
	Path          string `toml:"path"`
	MaxOpenConns  uint   `toml:"max_open_conns"`
	BusyTimeoutMs uint   `toml:"busy_timeout_ms"`
}

type PebbleConfig struct {
	CacheBytes uint64 `toml:"cache_bytes"`
}

type PostgresConfig struct {
	DSN               string `toml:"dsn"`
	DSNFile           string `toml:"dsn_file"`
	MaxOpenConns      uint   `toml:"max_open_conns"`
	MaxIdleConns      uint   `toml:"max_idle_conns"`
	ConnMaxLifetimeMs uint   `toml:"conn_max_lifetime_ms"`
}

type StoreConfig struct {
	Backend  string         `toml:"backend"`
	SQLite   SQLiteConfig   `toml:"sqlite"`
	Postgres PostgresConfig `toml:"postgres"`
	Pebble   PebbleConfig   `toml:"pebble"`
}

type RukConfig struct {
	BinaryPath             string   `toml:"binary_path"`
	RepoRoot               string   `toml:"repo_root"`
	SocketPath             string   `toml:"socket_path"`
	Socket                 string   `toml:"socket"`
	AllowedUIDs            []uint32 `toml:"allowed_uids"`
	MaxConnections         uint     `toml:"max_connections"`
	BlockingPoolSize       uint     `toml:"blocking_pool_size"`
	HandshakeTimeoutMs     uint64   `toml:"handshake_timeout_ms"`
	FrameTimeoutMs         uint64   `toml:"frame_timeout_ms"`
	StreamWindowBytes      uint64   `toml:"stream_window_bytes"`
	RetentionIntervalMs    uint64   `toml:"retention_interval_ms"`
	ClientDialTimeoutMs    uint     `toml:"client_dial_timeout_ms"`
	ClientPoolSize         uint     `toml:"client_pool_size"`
	ClientRequestTimeoutMs uint     `toml:"client_request_timeout_ms"`
	ClientPackTimeoutMs    uint     `toml:"client_pack_timeout_ms"`
	// StartupTimeoutMs bounds the socket-plus-handshake wait; it feeds
	// the supervisor probe and WaitReady. Restart attempts and backoff
	// are O35 constants (at most 3 restarts, 1 s, 2 s, 4 s) with no
	// config keys: setting them is an unknown key and fails startup.
	StartupTimeoutMs uint `toml:"startup_timeout_ms"`
}

type AuthConfig struct {
	TOTPIssuer        string `toml:"totp_issuer"`
	TokenPrefix       string `toml:"token_prefix"`
	PasswordMinLength uint   `toml:"password_min_length"`
	SessionTTWHours   uint   `toml:"session_ttl_hours"`
	Argon2MemoryKib   uint32 `toml:"argon2_memory_kib"`
	Argon2Iterations  uint32 `toml:"argon2_iterations"`
	Argon2Parallelism uint8  `toml:"argon2_parallelism"`
	TOTPRequired      bool   `toml:"totp_required"`
}

type OIDCProvider struct {
	Name             string   `toml:"name"`
	Issuer           string   `toml:"issuer"`
	ClientID         string   `toml:"client_id"`
	ClientSecretFile string   `toml:"client_secret_file"`
	RedirectURL      string   `toml:"redirect_url"`
	Scopes           []string `toml:"scopes"`
	AdminAllowList   []string `toml:"admin_allow_list"`
	AllowSignup      bool     `toml:"allow_signup"`
}

type CIRunnerConfig struct {
	BinaryPath   string   `toml:"binary_path"`
	WorkDir      string   `toml:"work_dir"`
	Sandbox      string   `toml:"sandbox"`
	Network      string   `toml:"network"`
	EnvAllowList []string `toml:"env_allow_list"`
	Concurrency  uint     `toml:"concurrency"`
}

type CIConfig struct {
	DefaultQueue      string         `toml:"default_queue"`
	Runner            CIRunnerConfig `toml:"runner"`
	JobTimeoutMs      uint64         `toml:"job_timeout_ms"`
	LogRetentionDays  uint           `toml:"log_retention_days"`
	ResultCacheBytes  uint64         `toml:"result_cache_bytes"`
	MaxConcurrentJobs uint           `toml:"max_concurrent_jobs"`
	AttestOnSuccess   bool           `toml:"attest_on_success"`
	Enabled           bool           `toml:"enabled"`
}

type MirrorsConfig struct {
	ReconcileIntervalMs uint64 `toml:"reconcile_interval_ms"`
	MaxConcurrentPushes uint   `toml:"max_concurrent_pushes"`
	PushTimeoutMs       uint64 `toml:"push_timeout_ms"`
	MaxRetries          uint   `toml:"max_retries"`
}

type WebhooksConfig struct {
	SigningKeyFile     string `toml:"signing_key_file"`
	DeliveryTimeoutMs  uint64 `toml:"delivery_timeout_ms"`
	MaxPayloadBytes    uint64 `toml:"max_payload_bytes"`
	MaxTargetsPerEvent uint   `toml:"max_targets_per_event"`
	MaxRetries         uint   `toml:"max_retries"`
	QueueDepth         uint   `toml:"queue_depth"`
	Enabled            bool   `toml:"enabled"`
}

type RateLimitConfig struct {
	RequestsPerMinute    uint `toml:"requests_per_minute"`
	AnonymousPerMinute   uint `toml:"anonymous_per_minute"`
	PushPerMinutePerRepo uint `toml:"push_per_minute_per_repo"`
	Burst                uint `toml:"burst"`
	BanMinutes           uint `toml:"ban_minutes"`
	Enabled              bool `toml:"enabled"`
}

type TelemetryConfig struct {
	OTLPEndpoint     string  `toml:"otlp_endpoint"`
	OTLPProtocol     string  `toml:"otlp_protocol"`
	ServiceName      string  `toml:"service_name"`
	Environment      string  `toml:"environment"`
	MetricsAddr      string  `toml:"metrics_addr"`
	TraceSampleRatio float64 `toml:"trace_sample_ratio"`
}

type Config struct {
	SecretKeyFile string          `toml:"secret_key_file"`
	DataDir       string          `toml:"data_dir"`
	Telemetry     TelemetryConfig `toml:"telemetry"`
	Log           LogConfig       `toml:"log"`
	OIDC          []OIDCProvider  `toml:"oidc"`
	Auth          AuthConfig      `toml:"auth"`
	Store         StoreConfig     `toml:"store"`
	SSH           SSHConfig       `toml:"ssh"`
	Webhooks      WebhooksConfig  `toml:"webhooks"`
	CI            CIConfig        `toml:"ci"`
	HTTP          HTTPConfig      `toml:"http"`
	Ruk           RukConfig       `toml:"ruk"`
	RateLimit     RateLimitConfig `toml:"rate_limit"`
	Mirrors       MirrorsConfig   `toml:"mirrors"`
}

func Defaults() Config {
	return Config{
		Log: LogConfig{Level: "info", Format: "json"},
		HTTP: HTTPConfig{
			Listen: []string{"127.0.0.1:3000"}, ReadTimeoutMs: 15000, ReadHeaderTimeoutMs: 10000,
			WriteTimeoutMs: 60000, IdleTimeoutMs: 120000, MaxRequestBodyBytes: 33554432, ShutdownDrainMs: 30000,
		},
		SSH:   SSHConfig{Listen: []string{"0.0.0.0:2222"}, Shell: "internal", MaxSessions: 128, AuthTimeoutMs: 30000, IdleTimeoutMs: 600000},
		Store: StoreConfig{Backend: "sqlite", SQLite: SQLiteConfig{MaxOpenConns: 16, BusyTimeoutMs: 5000}, Pebble: PebbleConfig{CacheBytes: 67108864}},
		Ruk: RukConfig{
			BinaryPath: "ruk", MaxConnections: 128, BlockingPoolSize: 16,
			HandshakeTimeoutMs: 5000, FrameTimeoutMs: 60000, StreamWindowBytes: 262144, RetentionIntervalMs: 60000,
			ClientDialTimeoutMs: 5000, ClientPoolSize: 32, ClientRequestTimeoutMs: 120000, ClientPackTimeoutMs: 1800000,
			StartupTimeoutMs: 15000,
		},
		Auth: AuthConfig{
			PasswordMinLength: 12, Argon2MemoryKib: 19456, Argon2Iterations: 2, Argon2Parallelism: 1,
			TOTPIssuer: "rukie", SessionTTWHours: 720, TokenPrefix: "rk_",
		},
		CI: CIConfig{
			DefaultQueue: "default", MaxConcurrentJobs: 8, JobTimeoutMs: 3600000, LogRetentionDays: 90,
			ResultCacheBytes: 10737418240, AttestOnSuccess: true, Enabled: true,
			Runner: CIRunnerConfig{BinaryPath: "rukie-runner", Concurrency: 2, Sandbox: "auto", Network: "off"},
		},
		Mirrors: MirrorsConfig{ReconcileIntervalMs: 300000, MaxConcurrentPushes: 4, PushTimeoutMs: 600000, MaxRetries: 5},
		Webhooks: WebhooksConfig{
			MaxTargetsPerEvent: 64, DeliveryTimeoutMs: 10000, MaxRetries: 8,
			MaxPayloadBytes: 1048576, QueueDepth: 4096, Enabled: true,
		},
		RateLimit: RateLimitConfig{RequestsPerMinute: 600, AnonymousPerMinute: 60, PushPerMinutePerRepo: 30, Burst: 50},
		Telemetry: TelemetryConfig{OTLPProtocol: "http/protobuf", ServiceName: "rukie", Environment: "prod", TraceSampleRatio: 0.1},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		names := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			names = append(names, k.String())
		}
		return Config{}, fmt.Errorf("config: unknown key %s", strings.Join(names, ", "))
	}
	dir := filepath.Dir(path)
	cfg.resolvePaths(dir)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) resolvePaths(dir string) {
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	c.DataDir = resolve(c.DataDir)
	c.SecretKeyFile = resolve(c.SecretKeyFile)
	if c.Store.SQLite.Path == "" {
		c.Store.SQLite.Path = filepath.Join(c.DataDir, "meta.sqlite")
	} else {
		c.Store.SQLite.Path = resolve(c.Store.SQLite.Path)
	}
	if c.Ruk.RepoRoot == "" {
		c.Ruk.RepoRoot = filepath.Join(c.DataDir, "repos")
	} else {
		c.Ruk.RepoRoot = resolve(c.Ruk.RepoRoot)
	}
	if c.Ruk.SocketPath == "" {
		c.Ruk.SocketPath = filepath.Join(c.DataDir, "run", "ruk.sock")
	} else {
		c.Ruk.SocketPath = resolve(c.Ruk.SocketPath)
	}
	if c.Ruk.Socket != "" {
		c.Ruk.Socket = resolve(c.Ruk.Socket)
	}
	if c.CI.Runner.WorkDir == "" {
		c.CI.Runner.WorkDir = filepath.Join(c.DataDir, "ci-work")
	} else {
		c.CI.Runner.WorkDir = resolve(c.CI.Runner.WorkDir)
	}
	for i := range c.SSH.HostKeyFiles {
		c.SSH.HostKeyFiles[i] = resolve(c.SSH.HostKeyFiles[i])
	}
	for i := range c.OIDC {
		c.OIDC[i].ClientSecretFile = resolve(c.OIDC[i].ClientSecretFile)
	}
	if c.Webhooks.SigningKeyFile != "" {
		c.Webhooks.SigningKeyFile = resolve(c.Webhooks.SigningKeyFile)
	}
	if c.Store.Postgres.DSNFile != "" {
		c.Store.Postgres.DSNFile = resolve(c.Store.Postgres.DSNFile)
	}
}

func (c *Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir is required")
	}
	if c.SecretKeyFile == "" {
		return fmt.Errorf("config: secret_key_file is required")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: log.level must be debug, info, warn, or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("config: log.format must be json or text")
	}
	if len(c.HTTP.Listen) == 0 {
		return fmt.Errorf("config: http.listen requires at least one address")
	}
	if c.HTTP.ReadTimeoutMs < 0 || c.HTTP.ReadHeaderTimeoutMs < 0 || c.HTTP.WriteTimeoutMs < 0 || c.HTTP.IdleTimeoutMs < 0 || c.HTTP.MaxRequestBodyBytes < 0 || c.HTTP.ShutdownDrainMs < 0 {
		return fmt.Errorf("config: http timeouts and body limits must be non-negative")
	}
	if c.HTTP.ReadHeaderTimeoutMs > c.HTTP.ReadTimeoutMs {
		return fmt.Errorf("config: http.read_header_timeout_ms must be <= http.read_timeout_ms")
	}
	if c.HTTP.BehindProxy && len(c.HTTP.TrustedProxies) == 0 {
		return fmt.Errorf("config: http.behind_proxy requires http.trusted_proxies")
	}
	if c.Store.Backend != "sqlite" {
		return fmt.Errorf("config: store.backend %q is not supported", c.Store.Backend)
	}
	if c.Ruk.HandshakeTimeoutMs == 0 || c.Ruk.FrameTimeoutMs == 0 {
		return fmt.Errorf("config: ruk handshake and frame timeouts must be nonzero")
	}
	if c.Ruk.StreamWindowBytes < 1 || c.Ruk.StreamWindowBytes > 16777216 {
		return fmt.Errorf("config: ruk.stream_window_bytes must be 1..16777216")
	}
	if c.Auth.TOTPRequired {
		return fmt.Errorf("config: auth.totp_required is not supported")
	}
	defaults := Defaults().Auth
	if c.Auth.TOTPIssuer != defaults.TOTPIssuer || c.Auth.TokenPrefix != defaults.TokenPrefix ||
		c.Auth.PasswordMinLength != defaults.PasswordMinLength || c.Auth.SessionTTWHours != defaults.SessionTTWHours ||
		c.Auth.Argon2MemoryKib != defaults.Argon2MemoryKib || c.Auth.Argon2Iterations != defaults.Argon2Iterations ||
		c.Auth.Argon2Parallelism != defaults.Argon2Parallelism {
		return fmt.Errorf("config: non-default auth parameters are not supported")
	}
	if c.Auth.PasswordMinLength < 1 {
		return fmt.Errorf("config: auth.password_min_length must be positive")
	}
	if c.Auth.Argon2MemoryKib == 0 || c.Auth.Argon2Iterations == 0 || c.Auth.Argon2Parallelism == 0 {
		return fmt.Errorf("config: argon2 parameters must be nonzero")
	}
	seenOIDC := map[string]bool{}
	for _, p := range c.OIDC {
		if p.Name == "" || p.Issuer == "" || p.ClientID == "" || p.ClientSecretFile == "" {
			return fmt.Errorf("config: oidc providers require name, issuer, client_id, and client_secret_file")
		}
		if seenOIDC[p.Name] {
			return fmt.Errorf("config: duplicate oidc provider %q", p.Name)
		}
		seenOIDC[p.Name] = true
	}
	if c.CI.Runner.Sandbox != "" {
		switch c.CI.Runner.Sandbox {
		case "auto", "none", "ns", "container":
		default:
			return fmt.Errorf("config: ci.runner.sandbox must be auto, none, ns, or container")
		}
	}
	if c.Telemetry.TraceSampleRatio < 0 || c.Telemetry.TraceSampleRatio > 1 {
		return fmt.Errorf("config: telemetry.trace_sample_ratio must be 0..1")
	}
	if c.SSH.Enabled && len(c.SSH.HostKeyFiles) == 0 {
		return fmt.Errorf("config: ssh.enabled requires ssh.host_key_files")
	}
	if c.SSH.Shell != "" && c.SSH.Shell != "internal" {
		return fmt.Errorf("config: ssh.shell must be internal")
	}
	return nil
}

func (c Config) Effective() string {
	var b strings.Builder
	enc := toml.NewEncoder(&b)
	_ = enc.Encode(c)
	return b.String()
}
