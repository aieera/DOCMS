// Package salesforce implements the Salesforce connector: OAuth, attach
// documents to records, bidirectional metadata sync.
package salesforce

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/providers"
)

// Connector implements Salesforce integration.
type Connector struct {
	providers.BaseOAuth
	instanceURL string
	log         zerolog.Logger
}

// New creates a Salesforce connector.
func New(clientID, clientSecret, instanceURL string, log zerolog.Logger) *Connector {
	return &Connector{
		BaseOAuth: providers.BaseOAuth{
			ProviderName:  "salesforce",
			AuthEndpoint:  instanceURL + "/services/oauth2/authorize",
			TokenEndpoint: instanceURL + "/services/oauth2/token",
			Scopes:        []string{"api", "refresh_token"},
			ClientID:      clientID,
			ClientSecret:  clientSecret,
			Log:           log,
		},
		instanceURL: strings.TrimRight(instanceURL, "/"),
		log:         log,
	}
}

func (c *Connector) Name() string { return "salesforce" }

// AttachDocument creates a ContentVersion linked to a Salesforce record.
func (c *Connector) AttachDocument(ctx context.Context, tokens *model.OAuthTokens, recordID, title string, fileBody io.Reader) (string, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return "", err
	}
	// Create ContentVersion via composite API.
	url := c.instanceURL + "/services/data/v59.0/sobjects/ContentVersion"
	payload := fmt.Sprintf(`{"Title":"%s","PathOnClient":"%s.pdf","FirstPublishLocationId":"%s"}`, title, title, recordID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("sf attach %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &result)
	return result.ID, nil
}

// SyncMetadata fetches Salesforce record fields and returns them for
// bidirectional metadata sync.
func (c *Connector) SyncMetadata(ctx context.Context, tokens *model.OAuthTokens, objectType, recordID string, fields []string) (map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/services/data/v59.0/sobjects/%s/%s?fields=%s",
		c.instanceURL, objectType, recordID, strings.Join(fields, ","))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}
