package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// EnvSkillsAPI overrides the skills.sh API base URL.
const EnvSkillsAPI = "FAEHIGKEITEN_SKILLS_API"

// DefaultSkillsAPI is the public skills.sh directory API.
const DefaultSkillsAPI = "https://skills.sh"

// RemoteSkill is a search hit from the skills.sh directory.
type RemoteSkill struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	// Source is the GitHub "owner/repo" containing the skill.
	Source string `json:"source"`
}

// RemoteSearch queries the skills.sh directory, which indexes skills across
// all of GitHub.
type RemoteSearch struct {
	BaseURL string
	Client  *http.Client
}

// NewRemoteSearch returns a client for skills.sh, honouring EnvSkillsAPI.
func NewRemoteSearch() *RemoteSearch {
	base := os.Getenv(EnvSkillsAPI)
	if base == "" {
		base = DefaultSkillsAPI
	}
	return &RemoteSearch{BaseURL: base, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Search returns up to limit skills matching query, most installed first.
func (r *RemoteSearch) Search(ctx context.Context, query string, limit int) ([]RemoteSkill, error) {
	q := url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+"/api/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh search: %s", resp.Status)
	}
	var body struct {
		Skills []RemoteSkill `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("skills.sh search: %w", err)
	}
	return body.Skills, nil
}
