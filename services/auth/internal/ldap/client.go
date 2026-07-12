// Package ldap is the LDAP/AD direct-bind integration for the auth
// service (ADR 0062).
//
// Three responsibilities:
//
//  1. Authenticate a user against the directory (bind-as-user).
//  2. Resolve the user's effective group memberships, including
//     nested groups (AD's LDAP_MATCHING_RULE_IN_CHAIN, OpenLDAP
//     recursive fallback with depth cap + visited set).
//  3. Provide a per-tenant connection pool for the search/scheduler
//     paths so each request doesn't pay TCP+TLS+bind setup cost.
//
// Plain ldap:// without STARTTLS is rejected unless the config
// explicitly opts in via AllowInsecure — this is surfaced in the
// admin UI as a red banner.
package ldap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	ldaplib "github.com/go-ldap/ldap/v3"
)

// Config is the parsed shape used by the client. The repo decrypts
// the sealed bind password before constructing one.
type Config struct {
	URL                  string
	UseStartTLS          bool
	AllowInsecure        bool
	BindDN               string
	BindPassword         string
	UserSearchBase       string
	UserSearchFilter     string // contains {username} placeholder
	EmailAttribute       string
	DisplayNameAttribute string
	GroupSearchBase      string
	GroupSearchFilter    string // contains {user_dn} placeholder
	NestedGroups         bool
	// Tunables — defaults applied by NewClient.
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	// TLS verification override. Real deployments leave this false;
	// tests against a self-signed slapd flip it on.
	InsecureSkipVerify bool
}

// Sentinels surfaced to callers.
var (
	// ErrInvalidCredentials maps to LDAP_INVALID_CREDENTIALS (49).
	// The login flow uses this to decide whether to fall back to
	// local password auth (per fallback_to_local).
	ErrInvalidCredentials = errors.New("ldap: invalid credentials")
	// ErrUserNotFound is returned when the search by username
	// yields zero results. Same fallback rules as InvalidCredentials.
	ErrUserNotFound = errors.New("ldap: user not found")
	// ErrInsecureRejected is returned at construction when the
	// caller asks for plain ldap:// without StartTLS and didn't
	// opt in via AllowInsecure.
	ErrInsecureRejected = errors.New("ldap: plain ldap:// requires AllowInsecure or use_starttls")
	// ErrAmbiguousUser is returned when the search filter matches
	// >1 entry; refusing the bind is safer than picking one.
	ErrAmbiguousUser = errors.New("ldap: search filter matched multiple users")
)

// Defaults applied when a Config field is zero.
const (
	defaultDialTimeout    = 5 * time.Second
	defaultRequestTimeout = 10 * time.Second
	maxNestedDepth        = 16
	maxNestedVisited      = 1000
)

// Client is a thin wrapper over an ldap connection. Stateless apart
// from the underlying connection; the pool handles reuse.
type Client struct {
	cfg  Config
	conn *ldaplib.Conn
}

// Dial opens a fresh LDAP connection per the Config. It does NOT
// bind — the caller decides whether to bind as the service account
// or as the end user. STARTTLS is negotiated here when requested.
func Dial(cfg Config) (*Client, error) {
	cfg = withDefaults(cfg)

	if err := validateScheme(cfg); err != nil {
		return nil, err
	}

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	conn, err := ldaplib.DialURL(cfg.URL,
		ldaplib.DialWithDialer(&net.Dialer{Timeout: cfg.DialTimeout}),
		ldaplib.DialWithTLSConfig(tlsCfg),
	)
	if err != nil {
		return nil, fmt.Errorf("ldap dial: %w", err)
	}
	conn.SetTimeout(cfg.RequestTimeout)

	if cfg.UseStartTLS && strings.HasPrefix(strings.ToLower(cfg.URL), "ldap://") {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap starttls: %w", err)
		}
	}

	return &Client{cfg: cfg, conn: conn}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// Healthy returns true if the underlying connection is still live.
// Used by the pool before reusing a stashed connection.
func (c *Client) Healthy() bool {
	if c == nil || c.conn == nil || c.conn.IsClosing() {
		return false
	}
	// WhoAmI is one of the cheapest ops; if it errors, the conn is
	// effectively dead.
	_, err := c.conn.WhoAmI(nil)
	return err == nil
}

// BindService binds as the configured service account so subsequent
// searches run with its read perms.
func (c *Client) BindService() error {
	if err := c.conn.Bind(c.cfg.BindDN, c.cfg.BindPassword); err != nil {
		if isInvalidCreds(err) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("ldap bind service: %w", err)
	}
	return nil
}

// AuthenticateUser is the login path: search-then-bind.
//
// Step 1: bind as the service account, search for the user using
// `user_search_filter`. Step 2: bind as that user with the supplied
// password. On success returns the user's DN, email, and display
// name.
//
// Returns ErrInvalidCredentials specifically for bind failures so
// the login flow can apply fallback_to_local without leaking other
// errors as "wrong password".
func (c *Client) AuthenticateUser(username, password string) (UserResult, error) {
	if password == "" {
		// LDAP allows anonymous bind with empty password — never let
		// that escalate to "successful login".
		return UserResult{}, ErrInvalidCredentials
	}

	if err := c.BindService(); err != nil {
		return UserResult{}, err
	}

	user, err := c.findUser(username)
	if err != nil {
		return UserResult{}, err
	}

	// Re-bind as the user with their plaintext password.
	if err := c.conn.Bind(user.DN, password); err != nil {
		if isInvalidCreds(err) {
			return UserResult{}, ErrInvalidCredentials
		}
		return UserResult{}, fmt.Errorf("ldap user bind: %w", err)
	}

	// Re-bind as service for any subsequent search the caller does.
	// Otherwise group searches inherit the user's perms which may
	// not see all groups.
	if err := c.BindService(); err != nil {
		return user, err
	}
	return user, nil
}

// UserResult is what AuthenticateUser/findUser return.
type UserResult struct {
	DN          string
	Username    string
	Email       string
	DisplayName string
}

func (c *Client) findUser(username string) (UserResult, error) {
	filter := strings.ReplaceAll(c.cfg.UserSearchFilter, "{username}", ldaplib.EscapeFilter(username))
	req := ldaplib.NewSearchRequest(
		c.cfg.UserSearchBase,
		ldaplib.ScopeWholeSubtree, ldaplib.NeverDerefAliases,
		2, // size limit — we want exactly 1; 2 lets us detect ambiguity
		int(c.cfg.RequestTimeout/time.Second), false,
		filter,
		[]string{"dn", c.cfg.EmailAttribute, c.cfg.DisplayNameAttribute},
		nil,
	)
	res, err := c.conn.Search(req)
	if err != nil {
		return UserResult{}, fmt.Errorf("ldap user search: %w", err)
	}
	if len(res.Entries) == 0 {
		return UserResult{}, ErrUserNotFound
	}
	if len(res.Entries) > 1 {
		return UserResult{}, ErrAmbiguousUser
	}
	e := res.Entries[0]
	return UserResult{
		DN:          e.DN,
		Username:    username,
		Email:       e.GetAttributeValue(c.cfg.EmailAttribute),
		DisplayName: e.GetAttributeValue(c.cfg.DisplayNameAttribute),
	}, nil
}

// ResolveGroups returns the DNs of every group the user belongs to,
// including transitively-reachable groups when NestedGroups=true.
//
// AD path: a single search using LDAP_MATCHING_RULE_IN_CHAIN.
// OpenLDAP/other path: recursive resolution with depth cap (16) and
// visited-set cap (1000) to prevent cycles and runaway fan-out.
func (c *Client) ResolveGroups(userDN string) ([]string, error) {
	if !c.cfg.NestedGroups {
		return c.directGroups(userDN)
	}

	// Try the AD matching rule first. If the directory rejects the
	// control we fall back to recursive resolution.
	dns, err := c.matchingRuleInChain(userDN)
	if err == nil {
		return dns, nil
	}
	// Any error from the matching-rule branch (including unsupported
	// control) falls through to the recursive resolver.
	return c.recursiveGroups(userDN)
}

// directGroups runs the configured group filter once.
func (c *Client) directGroups(userDN string) ([]string, error) {
	filter := strings.ReplaceAll(c.cfg.GroupSearchFilter, "{user_dn}", ldaplib.EscapeFilter(userDN))
	req := ldaplib.NewSearchRequest(
		c.cfg.GroupSearchBase, ldaplib.ScopeWholeSubtree, ldaplib.NeverDerefAliases,
		0, int(c.cfg.RequestTimeout/time.Second), false,
		filter, []string{"dn"}, nil,
	)
	res, err := c.conn.Search(req)
	if err != nil {
		return nil, fmt.Errorf("ldap group search: %w", err)
	}
	out := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, e.DN)
	}
	return out, nil
}

// matchingRuleInChain uses AD's LDAP_MATCHING_RULE_IN_CHAIN
// (1.2.840.113556.1.4.1941) to resolve nested groups in one round
// trip. Filter is constructed by replacing the literal
// `member={user_dn}` substring with `member:1.2.840.113556.1.4.1941:={user_dn}`.
// If the configured filter doesn't match that shape we fall back to
// a `(member:OID:={user_dn})` filter — same semantics, AD-specific.
func (c *Client) matchingRuleInChain(userDN string) ([]string, error) {
	filter := fmt.Sprintf("(member:1.2.840.113556.1.4.1941:=%s)", ldaplib.EscapeFilter(userDN))
	req := ldaplib.NewSearchRequest(
		c.cfg.GroupSearchBase, ldaplib.ScopeWholeSubtree, ldaplib.NeverDerefAliases,
		0, int(c.cfg.RequestTimeout/time.Second), false,
		filter, []string{"dn"}, nil,
	)
	res, err := c.conn.Search(req)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, e.DN)
	}
	return out, nil
}

// recursiveGroups walks parent groups breadth-first up to maxNestedDepth
// levels with a visited-set cap to prevent cycles. Each level issues a
// (|(member=dn1)(member=dn2)...) query so depth cost is O(depth), not
// O(groups).
func (c *Client) recursiveGroups(userDN string) ([]string, error) {
	visited := map[string]bool{}
	frontier := []string{userDN}

	for depth := 0; depth < maxNestedDepth && len(frontier) > 0; depth++ {
		// Build OR'd filter for this layer.
		var ors strings.Builder
		ors.WriteString("(|")
		for _, dn := range frontier {
			fmt.Fprintf(&ors, "(member=%s)", ldaplib.EscapeFilter(dn))
		}
		ors.WriteString(")")

		req := ldaplib.NewSearchRequest(
			c.cfg.GroupSearchBase, ldaplib.ScopeWholeSubtree, ldaplib.NeverDerefAliases,
			0, int(c.cfg.RequestTimeout/time.Second), false,
			ors.String(), []string{"dn"}, nil,
		)
		res, err := c.conn.Search(req)
		if err != nil {
			return nil, fmt.Errorf("ldap recursive group search: %w", err)
		}

		next := make([]string, 0, len(res.Entries))
		for _, e := range res.Entries {
			if visited[e.DN] {
				continue
			}
			if len(visited) >= maxNestedVisited {
				// Bail out with what we have. The caller logs a warning
				// and the partial set is still better than nothing.
				goto done
			}
			visited[e.DN] = true
			next = append(next, e.DN)
		}
		frontier = next
	}

done:
	out := make([]string, 0, len(visited))
	for dn := range visited {
		out = append(out, dn)
	}
	return out, nil
}

// MemberEntry is one user returned by SearchGroupMembers.
type MemberEntry struct {
	DN          string
	Email       string
	DisplayName string
}

// SearchGroupMembers returns the users (not groups) that belong to
// the given group DN — directly when NestedGroups=false, transitively
// when true.
//
// For AD this uses LDAP_MATCHING_RULE_IN_CHAIN against the user
// search base with `memberOf:OID:=<groupDN>`. The result is filtered
// to entries that look like users (presence of the email attribute);
// nested groups themselves don't carry that and are dropped.
//
// For directories that reject the matching-rule control, falls back
// to a non-nested `(memberOf={groupDN})` search — the recursive
// expansion option for sync-time member listing is left as a future
// optimisation; the typical AD deployment supports the in-chain rule.
func (c *Client) SearchGroupMembers(groupDN string) ([]MemberEntry, error) {
	if c.cfg.NestedGroups {
		entries, err := c.searchUsersByMatchingRule(groupDN)
		if err == nil {
			return entries, nil
		}
		// Any failure (including unsupported control) -> fall through.
	}
	return c.searchUsersByMemberOf(groupDN)
}

func (c *Client) searchUsersByMatchingRule(groupDN string) ([]MemberEntry, error) {
	filter := fmt.Sprintf("(memberOf:1.2.840.113556.1.4.1941:=%s)", ldaplib.EscapeFilter(groupDN))
	return c.searchUsersFiltered(filter)
}

func (c *Client) searchUsersByMemberOf(groupDN string) ([]MemberEntry, error) {
	filter := fmt.Sprintf("(memberOf=%s)", ldaplib.EscapeFilter(groupDN))
	return c.searchUsersFiltered(filter)
}

func (c *Client) searchUsersFiltered(filter string) ([]MemberEntry, error) {
	req := ldaplib.NewSearchRequest(
		c.cfg.UserSearchBase, ldaplib.ScopeWholeSubtree, ldaplib.NeverDerefAliases,
		0, int(c.cfg.RequestTimeout/time.Second), false,
		filter,
		[]string{"dn", c.cfg.EmailAttribute, c.cfg.DisplayNameAttribute},
		nil,
	)
	res, err := c.conn.Search(req)
	if err != nil {
		return nil, err
	}
	out := make([]MemberEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		email := e.GetAttributeValue(c.cfg.EmailAttribute)
		if email == "" {
			// Without an email we can't map to a local user — skip.
			continue
		}
		out = append(out, MemberEntry{
			DN:          e.DN,
			Email:       email,
			DisplayName: e.GetAttributeValue(c.cfg.DisplayNameAttribute),
		})
	}
	return out, nil
}

// ---- helpers --------------------------------------------------------------

func withDefaults(c Config) Config {
	if c.DialTimeout == 0 {
		c.DialTimeout = defaultDialTimeout
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.EmailAttribute == "" {
		c.EmailAttribute = "mail"
	}
	if c.DisplayNameAttribute == "" {
		c.DisplayNameAttribute = "displayName"
	}
	if c.UserSearchFilter == "" {
		c.UserSearchFilter = "(sAMAccountName={username})"
	}
	if c.GroupSearchFilter == "" {
		c.GroupSearchFilter = "(member={user_dn})"
	}
	return c
}

func validateScheme(c Config) error {
	url := strings.ToLower(c.URL)
	switch {
	case strings.HasPrefix(url, "ldaps://"):
		return nil
	case strings.HasPrefix(url, "ldap://"):
		if c.UseStartTLS || c.AllowInsecure {
			return nil
		}
		return ErrInsecureRejected
	default:
		return fmt.Errorf("ldap: unsupported scheme in url %q (want ldap:// or ldaps://)", c.URL)
	}
}

// isInvalidCreds checks for LDAP result code 49.
func isInvalidCreds(err error) bool {
	var lerr *ldaplib.Error
	if errors.As(err, &lerr) {
		return lerr.ResultCode == ldaplib.LDAPResultInvalidCredentials
	}
	return false
}
