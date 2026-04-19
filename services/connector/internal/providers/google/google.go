// Package google implements the Google Workspace connector: OAuth, Gmail
// ingestion, Drive migration.
package google

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
	"github.com/vaultdms/vaultdms/services/connector/internal/providers"
)

const gmailBaseURL = "https://gmail.googleapis.com/gmail/v1"
const driveBaseURL = "https://www.googleapis.com/drive/v3"

// Connector implements Google Workspace integration.
type Connector struct {
	providers.BaseOAuth
	log zerolog.Logger
}

// New creates a Google Workspace connector.
func New(clientID, clientSecret string, log zerolog.Logger) *Connector {
	return &Connector{
		BaseOAuth: providers.BaseOAuth{
			ProviderName:  "google_workspace",
			AuthEndpoint:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint: "https://oauth2.googleapis.com/token",
			Scopes:        []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/drive.readonly"},
			ClientID:      clientID,
			ClientSecret:  clientSecret,
			Log:           log,
		},
		log: log,
	}
}

func (c *Connector) Name() string { return "google_workspace" }

// PollGmail fetches recent unread messages.
func (c *Connector) PollGmail(ctx context.Context, tokens *model.OAuthTokens) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		gmailBaseURL+"/users/me/messages?q=is:unread&maxResults=20", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("gmail rate limited")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gmail %d: %s", resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	var result struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(body, &result)
	return result.Messages, nil
}

// ListDriveFiles lists files from Google Drive for migration.
func (c *Connector) ListDriveFiles(ctx context.Context, tokens *model.OAuthTokens, folderID string) ([]map[string]any, error) {
	tokens, err := c.EnsureValid(ctx, tokens)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		driveBaseURL+"/files?q="+q+"&fields=files(id,name,mimeType,size,modifiedTime)&pageSize=100", nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Files []map[string]any `json:"files"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.Files, nil
}
