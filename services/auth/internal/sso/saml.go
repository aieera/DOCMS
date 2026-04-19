package sso

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// SPKey + SPCert are the auth service's SAML Service Provider identity.
// In dev they're self-signed on startup; in prod callers load PEMs from
// their secret store and pass them into NewSAMLService.
type SPKeyMaterial struct {
	PrivateKey  *rsa.PrivateKey
	Certificate *x509.Certificate
}

// NewSelfSignedSP generates a 2048-bit RSA key + self-signed cert with a
// 1-year validity. Intended for dev only. Prod callers use LoadSPKeyMaterial.
func NewSelfSignedSP() (*SPKeyMaterial, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("rsa: %w", err)
	}
	now := time.Now()
	serial, err := generateSerial()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "vaultdms-auth-sp"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("x509: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &SPKeyMaterial{PrivateKey: key, Certificate: cert}, nil
}

// LoadSPKeyMaterial parses PEM-encoded private key + certificate. Errors if
// either is malformed or the key doesn't match the cert's public key.
func LoadSPKeyMaterial(keyPEM, certPEM []byte) (*SPKeyMaterial, error) {
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, vdmserr.Validation("", "sp key PEM: empty or malformed")
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, vdmserr.Validation("", "sp cert PEM: empty or malformed")
	}
	tlsKP, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("sp keypair: %w", err)
	}
	cert, err := x509.ParseCertificate(tlsKP.Certificate[0])
	if err != nil {
		return nil, err
	}
	rsaKey, ok := tlsKP.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, vdmserr.Validation("", "sp key is not RSA")
	}
	return &SPKeyMaterial{PrivateKey: rsaKey, Certificate: cert}, nil
}

// ---- Service --------------------------------------------------------------

// SAMLProvisioner is the contract the SAML service uses to find-or-create
// users after a successful assertion. The auth service's core
// *service.Service satisfies this (method added in saml_provision.go).
type SAMLProvisioner interface {
	FindOrCreateSAMLUser(ctx context.Context, tenantID uuid.UUID, email, displayName string, groups []string, ip, ua string) (sessionToken string, expiresAt time.Time, err error)
}

// Service exposes SAML 2.0 flows to HTTP handlers. It is stateless apart
// from the SP key material (generated / loaded once) and holds references
// to Redis (for AuthnRequest ID tracking) and the config repository.
type Service struct {
	sp        *SPKeyMaterial
	pool      *pgxpool.Pool
	rdb       *redis.Client
	cfgRepo   ConfigRepository
	prov      SAMLProvisioner
	log       zerolog.Logger
	publicURL string // https://app.example.com (no trailing slash)
	now       func() time.Time
}


// ServiceConfig bundles dependencies.
type ServiceConfig struct {
	SP          *SPKeyMaterial
	Pool        *pgxpool.Pool
	Redis       *redis.Client
	ConfigRepo  ConfigRepository
	Provisioner SAMLProvisioner
	Logger      zerolog.Logger
	PublicURL   string
}

// NewService constructs a SAML Service.
func NewService(cfg ServiceConfig) *Service {
	return &Service{
		sp:        cfg.SP,
		pool:      cfg.Pool,
		rdb:       cfg.Redis,
		cfgRepo:   cfg.ConfigRepo,
		prov:      cfg.Provisioner,
		log:       cfg.Logger,
		publicURL: strings.TrimRight(cfg.PublicURL, "/"),
		now:       time.Now,
	}
}

// ---- SP metadata ----------------------------------------------------------

// Metadata returns the SP metadata XML for the given tenant slug.
// EntityID and ACS URL are derived from publicURL + tenantSlug so an admin
// can feed this document directly into their IdP.
func (s *Service) Metadata(ctx context.Context, tenantSlug string) ([]byte, error) {
	sp, err := s.buildServiceProvider(ctx, tenantSlug, nil /* no IdP needed for metadata */)
	if err != nil {
		return nil, err
	}
	doc := sp.Metadata()
	return xmlBytes(doc)
}

// ---- SP-initiated login ---------------------------------------------------

// BuildAuthnRequest generates the AuthnRequest, stores the request ID in
// Redis for InResponseTo replay prevention, and returns the URL the browser
// should be redirected to.
//
// The request ID has a 10-minute TTL — well beyond most IdP response times
// but short enough that replay windows are minimal.
func (s *Service) BuildAuthnRequest(ctx context.Context, tenantSlug string, tenantID uuid.UUID) (redirectURL string, err error) {
	samlCfg, sp, err := s.providerForTenant(ctx, tenantSlug, tenantID)
	if err != nil {
		return "", err
	}
	_ = samlCfg

	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", fmt.Errorf("make authn request: %w", err)
	}

	// Track the request ID in Redis for InResponseTo validation.
	key := samlReqKey(req.ID)
	body, _ := json.Marshal(map[string]string{
		"tenant_slug": tenantSlug,
		"tenant_id":   tenantID.String(),
		"issued_at":   s.now().UTC().Format(time.RFC3339),
	})
	if err := s.rdb.Set(ctx, key, body, 10*time.Minute).Err(); err != nil {
		return "", fmt.Errorf("redis set saml req: %w", err)
	}

	u, err := req.Redirect("", sp)
	if err != nil {
		return "", fmt.Errorf("redirect build: %w", err)
	}
	return u.String(), nil
}

// ---- ACS (Assertion Consumer Service) -------------------------------------

// ACSResult is what the handler returns to the client: the session token
// (so it can set a cookie) and expiry.
type ACSResult struct {
	SessionToken string
	ExpiresAt    time.Time
	RedirectTo   string
}

// ConsumeAssertion parses a POST body from the IdP, validates it, and
// (on success) provisions the user + issues a session.
//
// Validation covers:
//  1. SAMLResponse is non-empty (form-posted, base64-decoded)
//  2. Signature matches the IdP certificate in our config
//  3. NotBefore / NotOnOrAfter within +/- clock skew (default 2 min)
//  4. Audience matches our SP EntityID
//  5. InResponseTo matches a request ID we stored (one-time pop via Redis GETDEL)
//  6. Assertion ID has not been seen before (crewjam tracks per-SP; we also
//     store the ID briefly in Redis for cross-instance replay prevention)
func (s *Service) ConsumeAssertion(ctx context.Context, r *http.Request, tenantSlug string, tenantID uuid.UUID, ip, ua string) (*ACSResult, error) {
	if err := r.ParseForm(); err != nil {
		return nil, vdmserr.Validation("saml_response", "could not parse form")
	}
	raw := r.PostForm.Get("SAMLResponse")
	if raw == "" {
		return nil, vdmserr.Validation("saml_response", "missing SAMLResponse")
	}
	samlCfg, sp, err := s.providerForTenant(ctx, tenantSlug, tenantID)
	if err != nil {
		return nil, err
	}

	// Extract InResponseTo up front so we can pop it from Redis atomically
	// regardless of downstream signature outcome — prevents replay either
	// way.
	inResponseTo, err := extractInResponseTo(raw)
	if err != nil {
		return nil, vdmserr.Validation("saml_response", "malformed XML")
	}
	if inResponseTo == "" {
		return nil, vdmserr.Validation("saml_response", "missing InResponseTo")
	}
	if err := s.consumeAuthnReqID(ctx, inResponseTo, tenantSlug); err != nil {
		return nil, err
	}

	// crewjam does signature + timestamp + audience validation.
	assertion, err := sp.ParseResponse(r, []string{inResponseTo})
	if err != nil {
		s.log.Warn().Err(err).Msg("saml parse response failed (signature/timestamp/audience)")
		return nil, vdmserr.ErrUnauthorized
	}

	// Cross-instance replay prevention on Assertion ID.
	if assertion.ID != "" {
		if seen, _ := s.markAssertionSeen(ctx, assertion.ID, samlCfg.clockSkew()); seen {
			s.log.Warn().Str("assertion_id", assertion.ID).Msg("saml assertion replay detected")
			return nil, vdmserr.ErrUnauthorized
		}
	}

	email, displayName, groups := extractAttributes(assertion, samlCfg.AttributeMapping)
	if email == "" {
		s.log.Warn().Msg("saml assertion missing email attribute")
		return nil, vdmserr.Validation("saml_response", "email attribute missing")
	}

	token, expiresAt, err := s.prov.FindOrCreateSAMLUser(ctx, tenantID, email, displayName, groups, ip, ua)
	if err != nil {
		return nil, err
	}

	// Where to land the browser. Callers can override via RelayState, but
	// we restrict to the app's own origin to defeat open-redirect attacks.
	redirectTo := s.publicURL + "/?sso=saml"
	if rs := r.PostForm.Get("RelayState"); rs != "" && isSafeRelay(rs, s.publicURL) {
		redirectTo = rs
	}
	return &ACSResult{SessionToken: token, ExpiresAt: expiresAt, RedirectTo: redirectTo}, nil
}

// ---- internals ------------------------------------------------------------

func (s *Service) providerForTenant(ctx context.Context, tenantSlug string, tenantID uuid.UUID) (*SAMLConfig, *saml.ServiceProvider, error) {
	cfg, err := s.cfgRepo.GetActiveByTenantProvider(ctx, s.pool, tenantID, ProviderSAML)
	if err != nil {
		return nil, nil, vdmserr.Validation("tenant_slug", "SAML not configured for tenant")
	}
	var samlCfg SAMLConfig
	if err := json.Unmarshal(cfg.Config, &samlCfg); err != nil {
		return nil, nil, fmt.Errorf("sso_config parse: %w", err)
	}
	sp, err := s.buildServiceProvider(ctx, tenantSlug, &samlCfg)
	if err != nil {
		return nil, nil, err
	}
	return &samlCfg, sp, nil
}

// buildServiceProvider constructs a crewjam saml.ServiceProvider. If
// samlCfg is non-nil we also fetch + parse IdP metadata.
func (s *Service) buildServiceProvider(ctx context.Context, tenantSlug string, samlCfg *SAMLConfig) (*saml.ServiceProvider, error) {
	acsURL, err := url.Parse(s.publicURL + "/api/v1/auth/saml/" + tenantSlug + "/acs")
	if err != nil {
		return nil, err
	}
	metaURL, err := url.Parse(s.publicURL + "/api/v1/auth/saml/" + tenantSlug + "/metadata")
	if err != nil {
		return nil, err
	}
	sp := &saml.ServiceProvider{
		EntityID:          metaURL.String(),
		Key:               s.sp.PrivateKey,
		Certificate:       s.sp.Certificate,
		MetadataURL:       *metaURL,
		AcsURL:            *acsURL,
		AllowIDPInitiated: false,
	}
	if samlCfg == nil {
		return sp, nil
	}
	idpMeta, err := s.loadIdPMetadata(ctx, *samlCfg)
	if err != nil {
		return nil, err
	}
	sp.IDPMetadata = idpMeta
	return sp, nil
}

// loadIdPMetadata pulls either the inline XML or the URL the admin provided.
// The samlsp helper handles HTTP fetching with sensible timeouts; for
// inline XML we parse manually.
func (s *Service) loadIdPMetadata(ctx context.Context, cfg SAMLConfig) (*saml.EntityDescriptor, error) {
	if cfg.IdPMetadataXML != "" {
		md, err := samlsp.ParseMetadata([]byte(cfg.IdPMetadataXML))
		if err != nil {
			return nil, fmt.Errorf("parse idp metadata xml: %w", err)
		}
		return md, nil
	}
	if cfg.IdPMetadataURL == "" {
		return nil, vdmserr.Validation("", "idp metadata URL/XML missing from tenant config")
	}
	u, err := url.Parse(cfg.IdPMetadataURL)
	if err != nil {
		return nil, fmt.Errorf("idp metadata url: %w", err)
	}
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := http.DefaultClient
	md, err := samlsp.FetchMetadata(fctx, client, *u)
	if err != nil {
		return nil, fmt.Errorf("fetch idp metadata: %w", err)
	}
	return md, nil
}

// consumeAuthnReqID atomically pops a request ID from Redis. Returns
// ErrUnauthorized on miss (unknown InResponseTo → replay or foreign IdP).
func (s *Service) consumeAuthnReqID(ctx context.Context, id, tenantSlug string) error {
	key := samlReqKey(id)
	body, err := s.rdb.GetDel(ctx, key).Bytes()
	if err == redis.Nil {
		return vdmserr.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("redis getdel: %w", err)
	}
	var rec struct{ TenantSlug string `json:"tenant_slug"` }
	if err := json.Unmarshal(body, &rec); err != nil {
		return vdmserr.ErrUnauthorized
	}
	if rec.TenantSlug != tenantSlug {
		// Request came from a different tenant's flow — treat as replay.
		return vdmserr.ErrUnauthorized
	}
	return nil
}

// markAssertionSeen is the cross-instance replay guard. Stores the
// assertion ID in Redis with SETNX; returns (true, nil) if already present.
func (s *Service) markAssertionSeen(ctx context.Context, id string, skew time.Duration) (seen bool, err error) {
	key := "saml_assertion:" + id
	ok, err := s.rdb.SetNX(ctx, key, "1", skew+10*time.Minute).Result()
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// extractInResponseTo parses just enough of the SAMLResponse to pull
// InResponseTo off the root element. Signature + full validation happen
// downstream in crewjam.
func extractInResponseTo(rawB64 string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return "", err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(decoded); err != nil {
		return "", err
	}
	root := doc.Root()
	if root == nil {
		return "", vdmserr.Validation("", "empty root")
	}
	return root.SelectAttrValue("InResponseTo", ""), nil
}

// extractAttributes pulls the fields defined by the AttributeMapping from
// the parsed assertion. Falls back to well-known defaults when a mapping
// key is blank.
func extractAttributes(a *saml.Assertion, m AttributeMapping) (email, displayName string, groups []string) {
	mapping := map[string]string{
		"email":        firstNonEmpty(m.Email, "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "email", "mail"),
		"display_name": firstNonEmpty(m.DisplayName, "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name", "name", "displayName"),
		"groups":       firstNonEmpty(m.Groups, "http://schemas.xmlsoap.org/claims/Group", "groups", "memberOf"),
	}
	for _, stmt := range a.AttributeStatements {
		for _, attr := range stmt.Attributes {
			switch {
			case matchesAny(attr, mapping["email"]) && email == "":
				email = firstValue(attr)
			case matchesAny(attr, mapping["display_name"]) && displayName == "":
				displayName = firstValue(attr)
			case matchesAny(attr, mapping["groups"]):
				for _, v := range attr.Values {
					if v.Value != "" {
						groups = append(groups, v.Value)
					}
				}
			}
		}
	}
	// Subject NameID is a common fallback for email.
	if email == "" && a.Subject != nil && a.Subject.NameID != nil {
		if v := strings.TrimSpace(a.Subject.NameID.Value); v != "" && strings.Contains(v, "@") {
			email = v
		}
	}
	return
}

func matchesAny(a saml.Attribute, wants string) bool {
	if wants == "" {
		return false
	}
	for _, w := range strings.Split(wants, "|") {
		if strings.EqualFold(a.Name, w) || strings.EqualFold(a.FriendlyName, w) {
			return true
		}
	}
	return false
}

func firstValue(a saml.Attribute) string {
	if len(a.Values) == 0 {
		return ""
	}
	return strings.TrimSpace(a.Values[0].Value)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isSafeRelay keeps RelayState from becoming an open-redirect. Only URLs
// under our own publicURL origin are accepted.
func isSafeRelay(rs, publicURL string) bool {
	if rs == "" {
		return false
	}
	u, err := url.Parse(rs)
	if err != nil {
		return false
	}
	base, err := url.Parse(publicURL)
	if err != nil {
		return false
	}
	return u.Host == "" /* relative path */ || u.Host == base.Host
}

// clockSkew normalizes the config value to a time.Duration with bounds.
func (c *SAMLConfig) clockSkew() time.Duration {
	secs := c.ClockSkewSeconds
	if secs <= 0 {
		secs = 120
	}
	if secs > 600 {
		secs = 600
	}
	return time.Duration(secs) * time.Second
}

func samlReqKey(id string) string { return "saml_req:" + id }

// xmlBytes serializes the SP metadata to UTF-8 XML with declaration.
// crewjam's EntityDescriptor implements xml.Marshaler.
func xmlBytes(doc *saml.EntityDescriptor) ([]byte, error) {
	b, err := xml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.Write(b)
	return buf.Bytes(), nil
}
