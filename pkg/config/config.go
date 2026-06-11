// Package config loads service configuration from (in priority order):
// environment variables, a YAML file, then compiled-in defaults. All values are
// captured in a strongly-typed struct with validation tags.
package config

import (
	"fmt"
	"os"
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
	// and document sharing URLs. Env: SEDOC_PUBLIC_URL.
	PublicURL string `mapstructure:"public_url"`

	// PolicyServiceAddr is the gRPC address of the policy service used
	// by document + storage for authorization checks. Env:
	// POLICY_SERVICE_ADDR (not SEDOC-prefixed). Default: policy:9090.
	PolicyServiceAddr string `mapstructure:"policy_service_addr"`

	// StorageServiceAddr is the gRPC address of the storage service, used
	// by the document service to proxy user-facing upload/download
	// operations as REST (the storage service itself is gRPC-only). Env:
	// STORAGE_SERVICE_ADDR. Default: storage:9090.
	StorageServiceAddr string `mapstructure:"storage_service_addr"`

	// DocumentServiceAddr is the gRPC address of the document service.
	// Used by the signature service to mint a new version when a
	// signed PDF lands from a third-party vendor (ADR 0071) or a QES
	// ceremony completes (ADR 0070). Default: "document:9090".
	DocumentServiceAddr string `mapstructure:"document_service_addr"`

	// WorkflowServiceAddr / CollaborationServiceAddr / AuditServiceAddr
	// are the gRPC addresses the GraphQL gateway (ADR 0074) calls
	// from its read resolvers. Each may be empty at boot — the
	// resolver returns the field as null + logs once. Defaults
	// follow the docker-compose service-name convention.
	WorkflowServiceAddr      string `mapstructure:"workflow_service_addr"`
	CollaborationServiceAddr string `mapstructure:"collaboration_service_addr"`
	AuditServiceAddr         string `mapstructure:"audit_service_addr"`
	// AuthServiceAddr is the gRPC address of the auth service. Used by
	// the document service's bulk-import dispatcher (ADR 0075) when an
	// imported BulkUser / BulkGroup needs to be created in auth's DB.
	// Empty → user/group bulk items return "auth service not configured"
	// without aborting the rest of the batch.
	AuthServiceAddr string `mapstructure:"auth_service_addr"`

	// S3PublicBase optionally overrides the S3 endpoint host in
	// presigned URLs when the service is behind a reverse proxy.
	// Env: SEDOC_S3_PUBLIC_BASE.
	S3PublicBase string `mapstructure:"s3_public_base"`

	// InternalAPIKey protects internal administrative endpoints
	// (currently the billing service /internal/v1 routes). Callers
	// present it in the X-API-Key header. Env: SEDOC_INTERNAL_API_KEY.
	InternalAPIKey string `mapstructure:"internal_api_key"`

	// StripeWebhookSecret is the whsec_... secret used to verify
	// Stripe webhook signatures. Env: STRIPE_WEBHOOK_SECRET (not
	// SEDOC-prefixed — follows Stripe convention).
	StripeWebhookSecret string `mapstructure:"stripe_webhook_secret"`

	// WebhookAllowPrivateTargets relaxes the connector's outbound-webhook
	// URL guard (ValidateURL) so a subscription may target a plain-http
	// and/or private/loopback/link-local address. The default (false)
	// enforces https + public-IP-only, which is correct for a public
	// multi-tenant SaaS (SSRF defence). Set true ONLY on self-hosted /
	// on-prem deployments where the webhook receiver legitimately lives on
	// a trusted private network (e.g. an internal ERP on a LAN). The HMAC
	// signature still authenticates every delivery; this flag only waives
	// the transport-level https + private-IP checks. Env:
	// SEDOC_WEBHOOK_ALLOW_PRIVATE.
	WebhookAllowPrivateTargets bool `mapstructure:"webhook_allow_private_targets"`

	// ---- SMTP (Wave 12.1) -------------------------------------------------
	// SMTPHost + SMTPPort + creds point at the outbound mail relay
	// the notification service uses for transactional emails
	// (DSR verification token, invite, password reset). Empty host
	// disables email sending — the service logs the would-be email
	// and continues. Env: SEDOC_SMTP_*.
	SMTPHost     string `mapstructure:"smtp_host"`
	SMTPPort     int    `mapstructure:"smtp_port"`
	SMTPUsername string `mapstructure:"smtp_username"`
	SMTPPassword string `mapstructure:"smtp_password"`
	SMTPFrom     string `mapstructure:"smtp_from"`
	// SMTPStartTLS toggles STARTTLS negotiation. Default true; set
	// false only for a dev relay like MailHog.
	SMTPStartTLS bool `mapstructure:"smtp_starttls"`

	// ---- QES / TSP (ADR 0070) ---------------------------------------------
	// Per-provider creds for the eIDAS QTSP integrations. Empty
	// fields → adapter is unconfigured and StartQES rejects requests
	// for that provider. Vault paths are documented in the QES
	// runbook; these struct fields just carry whatever the secret
	// store has resolved at boot.
	QESSwisscomBaseURL    string `mapstructure:"qes_swisscom_base_url"`
	QESSwisscomCustomerID string `mapstructure:"qes_swisscom_customer_id"`
	QESSwisscomCertPEM    string `mapstructure:"qes_swisscom_cert_pem"`
	QESSwisscomKeyPEM     string `mapstructure:"qes_swisscom_key_pem"`

	QESIntesiBaseURL      string `mapstructure:"qes_intesi_base_url"`
	QESIntesiClientID     string `mapstructure:"qes_intesi_client_id"`
	QESIntesiClientSecret string `mapstructure:"qes_intesi_client_secret"`
	QESIntesiPinnedCAPEM  string `mapstructure:"qes_intesi_pinned_ca_pem"`

	QESInfoCertBaseURL      string `mapstructure:"qes_infocert_base_url"`
	QESInfoCertClientID     string `mapstructure:"qes_infocert_client_id"`
	QESInfoCertClientSecret string `mapstructure:"qes_infocert_client_secret"`
	QESInfoCertOrgID        string `mapstructure:"qes_infocert_org_id"`

	// QESMockOK enables the in-memory ProviderMock adapter for CI +
	// Playwright. Refused outside of the dev/test environment by the
	// service-layer factory.
	QESMockOK bool `mapstructure:"qes_mock_ok"`

	// ---- ESign third-party connectors (ADR 0071) -------------------------
	// OAuth client credentials per vendor. Empty client_id → adapter
	// is disabled and StartOAuth refuses requests for that provider.
	ESignDocuSignClientID     string `mapstructure:"esign_docusign_client_id"`
	ESignDocuSignClientSecret string `mapstructure:"esign_docusign_client_secret"`
	ESignDocuSignAuthorizeURL string `mapstructure:"esign_docusign_authorize_url"`
	ESignDocuSignTokenURL     string `mapstructure:"esign_docusign_token_url"`
	ESignDocuSignRedirectURI  string `mapstructure:"esign_docusign_redirect_uri"`

	ESignAdobeSignClientID     string `mapstructure:"esign_adobe_sign_client_id"`
	ESignAdobeSignClientSecret string `mapstructure:"esign_adobe_sign_client_secret"`
	ESignAdobeSignAuthorizeURL string `mapstructure:"esign_adobe_sign_authorize_url"`
	ESignAdobeSignTokenURL     string `mapstructure:"esign_adobe_sign_token_url"`
	ESignAdobeSignRedirectURI  string `mapstructure:"esign_adobe_sign_redirect_uri"`

	// ESignStateHMAC seeds the OAuth-state HMAC. Must be ≥ 32 bytes
	// hex; service auto-generates one at boot if empty (logs once).
	ESignStateHMAC string `mapstructure:"esign_state_hmac"`
}

// Load reads configuration from (in order): env vars (SEDOC_* prefix),
// a config.yaml in the service working dir, and defaults. It validates the
// result and returns a populated Config or a descriptive error.
// mirrorLegacyEnv copies any VAULTDMS_*-prefixed environment variables to
// their SEDOC_* equivalents when the new name is unset. The project renamed
// its env prefix VAULTDMS_ -> SEDOC_; this shim keeps pre-rename .env files and
// already-deployed environments working through the transition. It is safe to
// remove once every environment injects SEDOC_* directly.
func mirrorLegacyEnv() {
	const oldPrefix, newPrefix = "VAULTDMS_", "SEDOC_"
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key := kv[:eq]
		if !strings.HasPrefix(key, oldPrefix) {
			continue
		}
		newKey := newPrefix + strings.TrimPrefix(key, oldPrefix)
		if _, ok := os.LookupEnv(newKey); !ok {
			_ = os.Setenv(newKey, kv[eq+1:])
		}
	}
}

func Load(serviceName string) (*Config, error) {
	mirrorLegacyEnv()

	v := viper.New()
	v.SetEnvPrefix("SEDOC")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/sedoc")

	setDefaults(v, serviceName)

	// Non-prefixed env bindings for variables that follow external
	// conventions (POLICY_SERVICE_ADDR, STRIPE_WEBHOOK_SECRET,
	// CLAMAV_ADDR, TEMPORAL_ADDR, OPENSEARCH_URL).
	_ = v.BindEnv("policy_service_addr", "POLICY_SERVICE_ADDR")
	_ = v.BindEnv("storage_service_addr", "STORAGE_SERVICE_ADDR")
	_ = v.BindEnv("document_service_addr", "DOCUMENT_SERVICE_ADDR")
	_ = v.BindEnv("workflow_service_addr", "WORKFLOW_SERVICE_ADDR")
	_ = v.BindEnv("collaboration_service_addr", "COLLABORATION_SERVICE_ADDR")
	_ = v.BindEnv("audit_service_addr", "AUDIT_SERVICE_ADDR")
	_ = v.BindEnv("auth_service_addr", "AUTH_SERVICE_ADDR")
	_ = v.BindEnv("stripe_webhook_secret", "STRIPE_WEBHOOK_SECRET")
	_ = v.BindEnv("webhook_allow_private_targets", "SEDOC_WEBHOOK_ALLOW_PRIVATE")
	_ = v.BindEnv("clamav_addr", "CLAMAV_ADDR")
	_ = v.BindEnv("temporal_addr", "TEMPORAL_ADDR")
	_ = v.BindEnv("opensearch_url", "OPENSEARCH_URL")

	// AutomaticEnv + Unmarshal only picks up keys that are already known
	// to viper (via SetDefault, Get, or an explicit BindEnv). The required
	// infra URLs are not defaulted (we want a clean startup failure when
	// they are missing), so bind them explicitly here.
	_ = v.BindEnv("database_url", "SEDOC_DATABASE_URL")
	_ = v.BindEnv("redis_url", "SEDOC_REDIS_URL")
	_ = v.BindEnv("redis_password", "SEDOC_REDIS_PASSWORD")
	_ = v.BindEnv("nats_url", "SEDOC_NATS_URL")
	_ = v.BindEnv("http_port", "SEDOC_HTTP_PORT")
	_ = v.BindEnv("grpc_port", "SEDOC_GRPC_PORT")
	_ = v.BindEnv("health_port", "SEDOC_HEALTH_PORT")
	// Per CLAUDE.md the S3-vs-MinIO naming sweep moved compose to
	// SEDOC_S3_*; the struct fields kept their MinIO* names. Bind
	// both spellings so either env wins (S3_* preferred — that's what
	// compose ships today). Without this the storage service starts
	// with cfg.MinIOEndpoint == "" and crashes at minio.New.
	_ = v.BindEnv("minio_endpoint", "SEDOC_S3_ENDPOINT", "SEDOC_MINIO_ENDPOINT")
	_ = v.BindEnv("minio_access_key", "SEDOC_S3_ACCESS_KEY", "SEDOC_MINIO_ACCESS_KEY")
	_ = v.BindEnv("minio_secret_key", "SEDOC_S3_SECRET_KEY", "SEDOC_MINIO_SECRET_KEY")
	_ = v.BindEnv("local_kek", "SEDOC_LOCAL_KEK")
	_ = v.BindEnv("public_url", "SEDOC_PUBLIC_URL")
	_ = v.BindEnv("internal_api_key", "SEDOC_INTERNAL_API_KEY")
	_ = v.BindEnv("s3_public_base", "SEDOC_S3_PUBLIC_BASE")

	// ADR 0071 — eSign connector envs. AutomaticEnv() only reads keys
	// viper already knows about; without these explicit binds a
	// Helm-supplied SEDOC_ESIGN_DOCUSIGN_CLIENT_ID never lands.
	// Per-tenant DB credentials are the primary path; these envs are
	// the optional deployment-wide fallback.
	_ = v.BindEnv("esign_state_hmac", "SEDOC_ESIGN_STATE_HMAC")
	_ = v.BindEnv("esign_docusign_client_id", "SEDOC_ESIGN_DOCUSIGN_CLIENT_ID")
	_ = v.BindEnv("esign_docusign_client_secret", "SEDOC_ESIGN_DOCUSIGN_CLIENT_SECRET")
	_ = v.BindEnv("esign_docusign_authorize_url", "SEDOC_ESIGN_DOCUSIGN_AUTHORIZE_URL")
	_ = v.BindEnv("esign_docusign_token_url", "SEDOC_ESIGN_DOCUSIGN_TOKEN_URL")
	_ = v.BindEnv("esign_docusign_redirect_uri", "SEDOC_ESIGN_DOCUSIGN_REDIRECT_URI")
	_ = v.BindEnv("esign_adobe_sign_client_id", "SEDOC_ESIGN_ADOBE_SIGN_CLIENT_ID")
	_ = v.BindEnv("esign_adobe_sign_client_secret", "SEDOC_ESIGN_ADOBE_SIGN_CLIENT_SECRET")
	_ = v.BindEnv("esign_adobe_sign_authorize_url", "SEDOC_ESIGN_ADOBE_SIGN_AUTHORIZE_URL")
	_ = v.BindEnv("esign_adobe_sign_token_url", "SEDOC_ESIGN_ADOBE_SIGN_TOKEN_URL")
	_ = v.BindEnv("esign_adobe_sign_redirect_uri", "SEDOC_ESIGN_ADOBE_SIGN_REDIRECT_URI")

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
		missing = append(missing, "SEDOC_PUBLIC_URL")
	}
	if c.LocalKEK == "" && c.KMSProvider == "local" {
		missing = append(missing, "SEDOC_LOCAL_KEK (kms_provider=local)")
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
	v.SetDefault("document_service_addr", "document:9090")
	v.SetDefault("workflow_service_addr", "workflow:9090")
	v.SetDefault("collaboration_service_addr", "collaboration:9090")
	v.SetDefault("audit_service_addr", "audit:9090")
	v.SetDefault("auth_service_addr", "auth:9090")
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
