// Package microsoft implements the Microsoft 365 connector: Graph API OAuth,
// email ingestion, SharePoint migration, Teams notifications.
package microsoft

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers"
)

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// Connector implements the Microsoft 365 integration.
type Connector struct {
	providers.BaseOAuth
	log zerolog.Logger
}

// New creates a Microsoft connector.
func New(clientID, clientSecret, tenantID string, log zerolog.Logger) *Connector {
	return &Connector{
		BaseOAuth: providers.BaseOAuth{
			ProviderName:  "microsoft365",
			AuthEndpoint:  fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", tenantID),
			TokenEndpoint: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID),
			Scopes:        []string{"Mail.Read", "Files.ReadWrite.All", "ChannelMessage.Send", "offline_access"},
			ClientID:      clientID,
			ClientSecret:  clientSecret,
			Log:           log,
		},
		log: log,
	}
}

func (c *Connector) Name() string { return "microsoft365" }

// PollInbox fetches unread emails and returns them for ingestion.
func (c *Connector) PollInbox(ctx context.Context, tokens *model.OAuthTokens) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		graphBaseURL+"/me/mailFolders/inbox/messages?$filter=isRead eq false&$top=20&$select=id,subject,from,receivedDateTime,body", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph inbox: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 429 {
		retryAfter := resp.Header.Get("Retry-After")
		return nil, fmt.Errorf("rate limited, retry after %s", retryAfter)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("graph inbox %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	var result struct {
		Value []map[string]any `json:"value"`
	}
	_ = json.Unmarshal(body, &result)
	return result.Value, nil
}

// SharePointListFiles lists files from a SharePoint site for bulk migration.
func (c *Connector) SharePointListFiles(ctx context.Context, tokens *model.OAuthTokens, siteID, driveID string) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/sites/%s/drives/%s/root/children", graphBaseURL, siteID, driveID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Value []map[string]any `json:"value"`
	}
	_ = json.Unmarshal(body, &result)
	return result.Value, nil
}

// SendTeamsNotification posts a message to a Teams channel.
func (c *Connector) SendTeamsNotification(ctx context.Context, tokens *model.OAuthTokens, teamID, channelID, message string) error {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/teams/%s/channels/%s/messages", graphBaseURL, teamID, channelID)
	payload := fmt.Sprintf(`{"body":{"content":"%s"}}`, message)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, io.NopCloser(
		&readerStr{s: payload, i: 0},
	))
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("teams message %d", resp.StatusCode)
	}
	return nil
}

type readerStr struct {
	s string
	i int
}

func (r *readerStr) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// silence
var _ = time.Now
