// Package gitops opens remediation pull requests. MeshMedic never writes to
// the cluster; this package is its only side effect, and the result is a
// reviewable pull request in the config repository.
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// Client opens pull requests against one GitHub config repository.
type Client struct {
	api   string
	owner string
	repo  string
	token string
	http  *http.Client
}

// NewClient builds a client for owner/repo. An empty apiBase means the
// public GitHub API; tests point it at a stub server.
func NewClient(apiBase, ownerRepo, token string) (*Client, error) {
	owner, repo, ok := strings.Cut(ownerRepo, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("gitops repo must be owner/repo, got %q", ownerRepo)
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Client{
		api:   strings.TrimRight(apiBase, "/"),
		owner: owner,
		repo:  repo,
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// PullRequest is one remediation change to propose.
type PullRequest struct {
	Branch        string
	Base          string // empty means the repository's default branch
	Title         string
	Body          string
	Path          string // file path inside the config repository
	Content       []byte // the rendered patch
	CommitMessage string
}

// Open creates the branch from base, commits the patch file, and opens the
// pull request. It returns the PR's HTML URL.
func (c *Client) Open(ctx context.Context, pr PullRequest) (string, error) {
	base := pr.Base
	if base == "" {
		var repoInfo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", c.owner, c.repo), nil, &repoInfo); err != nil {
			return "", fmt.Errorf("resolving default branch: %w", err)
		}
		base = repoInfo.DefaultBranch
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", c.owner, c.repo, base), nil, &ref); err != nil {
		return "", fmt.Errorf("resolving base %s: %w", base, err)
	}

	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", c.owner, c.repo), map[string]string{
		"ref": "refs/heads/" + pr.Branch,
		"sha": ref.Object.SHA,
	}, nil); err != nil {
		return "", fmt.Errorf("creating branch %s: %w", pr.Branch, err)
	}

	// If the patch file already exists (a previous remediation merged),
	// the contents API needs its blob sha to update instead of create.
	sha := ""
	var existing struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, pr.Path, pr.Branch), nil, &existing)
	switch {
	case err == nil:
		sha = existing.SHA
	case !isNotFound(err):
		return "", fmt.Errorf("checking %s: %w", pr.Path, err)
	}

	payload := map[string]string{
		"message": pr.CommitMessage,
		"content": base64.StdEncoding.EncodeToString(pr.Content),
		"branch":  pr.Branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	if err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/contents/%s", c.owner, c.repo, pr.Path), payload, nil); err != nil {
		return "", fmt.Errorf("committing %s: %w", pr.Path, err)
	}

	var opened struct {
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", c.owner, c.repo), map[string]string{
		"title": pr.Title,
		"head":  pr.Branch,
		"base":  base,
		"body":  pr.Body,
	}, &opened); err != nil {
		return "", fmt.Errorf("opening pull request: %w", err)
	}
	return opened.HTMLURL, nil
}

// BranchFor names the remediation branch. The timestamp keeps one breach
// episode from colliding with the next one for the same scenario.
func BranchFor(scenarioID string, t time.Time) string {
	return fmt.Sprintf("meshmedic/%s-%d", scenarioID, t.Unix())
}

// PathFor renders the configured path template with the incident parameters
// plus a "scenario" key holding the scenario ID.
func PathFor(tmpl string, params map[string]string, scenarioID string) (string, error) {
	values := map[string]string{"scenario": scenarioID}
	for k, v := range params {
		values[k] = v
	}
	t, err := template.New("path").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("path template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, values); err != nil {
		return "", fmt.Errorf("path template: %w", err)
	}
	return buf.String(), nil
}

type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusNotFound
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("github: reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 1024 {
			msg = msg[:1024] + "..."
		}
		return &apiError{status: resp.StatusCode, body: msg}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
