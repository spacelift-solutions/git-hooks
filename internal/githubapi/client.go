package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	defaultAPIURL = "https://api.github.com"
	apiVersion    = "2022-11-28"
)

// Client is a small GitHub REST API client for the endpoints used by the
// repository setup command.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a GitHub API client. Empty apiURL and httpClient values use
// the public GitHub API and http.DefaultClient, respectively.
func NewClient(apiURL, token string, httpClient *http.Client) *Client {
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		baseURL:    strings.TrimRight(apiURL, "/"),
		token:      token,
		httpClient: httpClient,
	}
}

// APIError describes a non-successful GitHub API response.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("GitHub API %s %s returned %s", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("GitHub API %s %s returned %s: %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound reports whether err is a GitHub API 404 response.
func IsNotFound(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound
}

// Do sends a JSON request and optionally decodes a JSON response.
func (c *Client) Do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode GitHub API request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	response, err := c.request(ctx, method, path, body, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode GitHub API response for %s %s: %w", method, path, err)
	}
	return nil
}

// GetRaw fetches an endpoint using GitHub's raw media type.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	response, err := c.request(ctx, http.MethodGet, path, nil, "application/vnd.github.raw+json")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read GitHub API response for GET %s: %w", path, err)
	}
	return body, nil
}

func (c *Client) request(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
	accept string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create GitHub API request: %w", err)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("GitHub API %s %s: %w", method, path, err)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, fmt.Errorf(
			"GitHub API %s %s returned %s; read response: %w",
			method,
			path,
			response.Status,
			readErr,
		)
	}
	return nil, &APIError{
		Method:     method,
		Path:       path,
		StatusCode: response.StatusCode,
		Status:     response.Status,
		Body:       strings.TrimSpace(string(responseBody)),
	}
}
