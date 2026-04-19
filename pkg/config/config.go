// Package config loads service configuration from (in priority order):
// environment variables, a YAML file, then compiled-in defaults. All values are
// captured in a strongly-typed struct with validation tags.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
)

// Config is the full service configuration. Every service embeds this and
// extends it with its own typed section when needed.
type Config struct {
	ServiceName    string `mapstructure:"service_name"    validate:"required"`
	ServiceVersion string `mapstructure:"service_version" validate:"required"`
	Environment    string `mapstructure:"environment"     validate:"oneof=dev staging prod"`
	LogLevel       string `mapstructure:"log_level"       validate:"oneof=debug info warn error"`
	Region         string `mapstructure:"region"          validate:"required"`

	HTTPPort   int `mapstructure:"http_port"   validate:"gt=0,lt=65536"`
	GRPCPort   int `mapstructure:"grpc_port"   validate:"gt=0,lt=65536"`
	HealthPort int `mapstructure:"health_port" validate:"gt=0,lt=65536"`

	DatabaseURL     string        `mapstructure:"database_url"      validate:"required,url"`
	DatabaseMaxConn int32         `mapstructure:"database_max_conn" validate:"gt=0"`
	DatabaseMinConn int32         `mapstructure:"database_min_conn" validate:"gte=0"`
	DatabaseTimeout time.Duration `mapstructure:"database_timeout"`

	RedisURL      string `mapstructure:"redis_url"      validate:"required"`
	RedisPassword string `mapstructure:"redis_password"`
	RedisDB       int    `mapstructure:"redis_db"       validate:"gte=0"`

	NATSURL string `mapstructure:"nats_url" validate:"required"`

	MinIOEndpoint  string `mapstructure:"minio_endpoint"`
	MinIOAccessKey string `mapstructure:"minio_access_key"`
	MinIOSecretKey string `mapstructure:"minio_secret_key"`
	MinIOUseSSL    bool   `mapstructure:"minio_use_ssl"`

	OpenSearchURL      string `mapstructure:"opensearch_url"`
	OpenSearchUsername string `mapstructure:"opensearch_username"`
	OpenSearchPassword string `mapstructure:"opensearch_password"`

	QdrantURL string `mapstructure:"qdrant_url"`

	TemporalAddr string `mapstructure:"temporal_addr"`
	ClamAVAddr   string `mapstructure:"clamav_addr"`

	KMSProvider string `mapstructure:"kms_provider" validate:"oneof=local vault aws"`
	LocalKEK    string `mapstructure:"local_kek"`

	DefaultRateLimitPerMin int `mapstructure:"default_rate_limit_per_min" validate:"gt=0"`

	// ---- Service-integration fields (moved out of os.Getenv call sites) ----

	// PublicURL is the externally reachable base URL of the platform
	// (e.g. https://app.vaultdms.io). Used by auth SSO redirect builders
	// and document sharing URLs. Env: VAULTDMS_PUBLIC_URL.
	PublicURL string `mapstructure:"public_url"`

	// PolicyServiceAddr is the gRPC address of the policy service used
	// by document + storage for authorization checks. Env:
	// POLICY_SERVICE_ADDR (not VAULTDMS-prefixed). Default: policy:9090.
	PolicyServiceAddr string `mapstructure:"policy_service_addr"`

	// StorageServiceAddr is the gRPC address of the storage service, used
	// by the document service to proxy user-facing upload/download
	// operations as REST (the storage service itself is gRPC-only). Env:
	// STORAGE_SERVICE_ADDR. Default: storage:9090.
	StorageServiceAddr string `mapstructure:"storage_service_addr"`

	// S3PublicBase optionally overrides the S3 endpoint host in
	// presigned URLs when the service is behind a reverse proxy.
	// Env: VAULTDMS_S3_PUBLIC_BASE.
	S3PublicBase string `mapstructure:"s3_public_base"`

	// InternalAPIKey protects internal administrative endpoints
	// (currently the billing service /internal/v1 routes). Callers
	// present it in the X-API-Key header. Env: VAULTDMS_INTERNAL_API_KEY.
	InternalAPIKey string `mapstructure:"internal_api_key"`

	// StripeWebhookSecret is the whsec_... secret used to verify
	// Stripe webhook signatures. Env: STRIPE_WEBHOOK_SECRET (not
	// VAULTDMS-prefixed — follows Stripe convention).
	StripeWebhookSecret string `mapstructure:"stripe_webhook_secret"`

	// ---- SMTP (Wave 12.1) -------------------------------------------------
	// SMTPHost + SMTPPort + creds point at the outbound mail relay
	// the notification service uses for transactional emails
	// (DSR verification token, invite, password reset). Empty host
	// disables email sending — the service logs the would-be email
	// and continues. Env: VAULTDMS_SMTP_*.
	SMTPHost     string `mapstructure:"smtp_host"`
	SMTPPort     int    `mapstructure:"smtp_port"`
	SMTPUsername string `mapstructure:"smtp_username"`
	SMTPPassword string `mapstructure:"smtp_password"`
	SMTPFrom     string `mapstructure:"smtp_from"`
	// SMTPStartTLS toggles STARTTLS negotiation. Default true; set
	// false only for a dev relay like MailHog.
	SMTPStartTLS bool `mapstructure:"smtp_starttls"`
}

// Load reads configuration from (in order): env vars (VAULTDMS_* prefix),
// a config.yaml in the service working dir, and defaults. It validates the
// result and returns a populated Config or a descriptive error.
func Load(serviceName string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("VAULTDMS")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/vaultdms")

	setDefaults(v, serviceName)

	// Non-prefixed env bindings for variables that follow external
	// conventions (POLICY_SERVICE_ADDR, STRIPE_WEBHOOK_SECRET,
	// CLAMAV_ADDR, TEMPORAL_ADDR, OPENSEARCH_URL).
	_ = v.BindEnv("policy_service_addr", "POLICY_SERVICE_ADDR")
	_ = v.BindEnv("storage_service_addr", "STORAGE_SERVICE_ADDR")
	_ = v.BindEnv("stripe_webhook_secret", "STRIPE_WEBHOOK_SECRET")
	_ = v.BindEnv("clamav_addr", "CLAMAV_ADDR")
	_ = v.BindEnv("temporal_addr", "TEMPORAL_ADDR")
	_ = v.BindEnv("opensearch_url", "OPENSEARCH_URL")

	// AutomaticEnv + Unmarshal only picks up keys that are already known
	// to viper (via SetDefault, Get, or an explicit BindEnv). The required
	// infra URLs are not defaulted (we want a clean startup failure when
	// they are missing), so bind them explicitly here.
	_ = v.BindEnv("database_url", "VAULTDMS_DATABASE_URL")
	_ = v.BindEnv("redis_url", "VAULTDMS_REDIS_URL")
	_ = v.BindEnv("redis_password", "VAULTDMS_REDIS_PASSWORD")
	_ = v.BindEnv("nats_url", "VAULTDMS_NATS_URL")
	_ = v.BindEnv("http_port", "VAULTDMS_HTTP_PORT")
	_ = v.BindEnv("grpc_port", "VAULTDMS_GRPC_PORT")
	_ = v.BindEnv("health_port", "VAULTDMS_HEALTH_PORT")
	_ = v.BindEnv("minio_endpoint", "VAULTDMS_MINIO_ENDPOINT")
	_ = v.BindEnv("minio_access_key", "VAULTDMS_MINIO_ACCESS_KEY")
	_ = v.BindEnv("minio_secret_key", "VAULTDMS_MINIO_SECRET_KEY")
	_ = v.BindEnv("local_kek", "VAULTDMS_LOCAL_KEK")
	_ = v.BindEnv("public_url", "VAULTDMS_PUBLIC_URL")
	_ = v.BindEnv("internal_api_key", "VAULTDMS_INTERNAL_API_KEY")
	_ = v.BindEnv("s3_public_base", "VAULTDMS_S3_PUBLIC_BASE")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := validator.New().Struct(&cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate enforces production-only requirements that cannot be expressed
// as struct tags (because they differ between dev and prod). Production
// deployments MUST set every secret / public URL explicitly; dev/staging
// may leave them empty and fall back to local defaults.
func (c *Config) Validate() error {
	if c.Environment != "prod" {
		return nil
	}
	var missing []string
	if c.PublicURL == "" {
		missing = append(missing, "VAULTDMS_PUBLIC_URL")
	}
	if c.LocalKEK == "" && c.KMSProvider == "local" {
		missing = append(missing, "VAULTDMS_LOCAL_KEK (kms_provider=local)")
	}
	// StripeWebhookSecret + InternalAPIKey are only required for services
	// that actually use them. Callers that need them should check cfg
	// directly; we enforce at the service boundary rather than globally
	// here so that non-billing services can boot without a Stripe secret.
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required prod env vars: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// RequireSecret is a helper for services that depend on a specific secret
// being non-empty (e.g. billing requires StripeWebhookSecret). Call from
// main.go after Load().
func (c *Config) RequireSecret(name, value string) error {
	if c.Environment != "prod" {
		return nil
	}
	if value == "" {
		return fmt.Errorf("config: %s must be set in production", name)
	}
	return nil
}

func setDefaults(v *viper.Viper, serviceName string) {
	v.SetDefault("service_name", serviceName)
	v.SetDefault("service_version", "dev")
	v.SetDefault("environment", "dev")
	v.SetDefault("log_level", "info")
	v.SetDefault("region", "us-east-1")

	v.SetDefault("http_port", 8080)
	v.SetDefault("grpc_port", 9090)
	v.SetDefault("health_port", 8081)

	v.SetDefault("database_max_conn", 50)
	v.SetDefault("database_min_conn", 5)
	v.SetDefault("database_timeout", 10*time.Second)

	v.SetDefault("redis_db", 0)

	v.SetDefault("kms_provider", "local")
	v.SetDefault("default_rate_limit_per_min", 1000)

	// Defaults safe to ship: infra addresses within the dev cluster.
	// Secrets (LocalKEK, StripeWebhookSecret, InternalAPIKey, PublicURL)
	// are intentionally NOT defaulted — they must fail at startup in prod.
	v.SetDefault("policy_service_addr", "policy:9090")
	v.SetDefault("storage_service_addr", "storage:9090")
	v.SetDefault("clamav_addr", "clamav:3310")
	v.SetDefault("temporal_addr", "temporal:7233")
	v.SetDefault("opensearch_url", "http://opensearch:9200")

	// SMTP defaults: empty host disables the real sender and the
	// notification service falls back to "would send" logging.
	// STARTTLS defaults to true so prod-like MTAs work without
	// additional config; dev relays (MailHog) override to false.
	v.SetDefault("smtp_port", 587)
	v.SetDefault("smtp_starttls", true)
	v.SetDefault("smtp_from", "noreply@vaultdms.local")
}

// MustLoad is like Load but panics on error. Suitable for use in main().
func MustLoad(serviceName string) *Config {
	cfg, err := Load(serviceName)
	if err != nil {
		panic(fmt.Errorf("config load failed: %w", err))
	}
	return cfg
}
