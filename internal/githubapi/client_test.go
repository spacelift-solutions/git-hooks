package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientDoSendsHeadersAndJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("X-GitHub-Api-Version") != apiVersion {
			t.Errorf("API version = %q", request.Header.Get("X-GitHub-Api-Version"))
		}
		var input map[string]string
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if input["name"] != "value" {
			t.Errorf("request body = %#v", input)
		}
		_, _ = writer.Write([]byte(`{"id":42}`))
	}))
	defer server.Close()

	var output struct {
		ID int `json:"id"`
	}
	client := NewClient(server.URL+"/", "secret", server.Client())
	err := client.Do(
		context.Background(),
		http.MethodPost,
		"/resource",
		map[string]string{"name": "value"},
		&output,
	)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if output.ID != 42 {
		t.Fatalf("output ID = %d, want 42", output.ID)
	}
}

func TestClientGetRaw(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Accept") != "application/vnd.github.raw+json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		_, _ = writer.Write([]byte("raw contents\n"))
	}))
	defer server.Close()

	content, err := NewClient(server.URL, "", server.Client()).GetRaw(
		context.Background(),
		"/contents/file",
	)
	if err != nil {
		t.Fatalf("GetRaw() error = %v", err)
	}
	if string(content) != "raw contents\n" {
		t.Fatalf("GetRaw() = %q", content)
	}
}

func TestClientReturnsTypedAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(writer, `{"message":"missing"}`, http.StatusNotFound)
	}))
	defer server.Close()

	err := NewClient(server.URL, "", server.Client()).Do(
		context.Background(),
		http.MethodGet,
		"/missing",
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("Do() unexpectedly succeeded")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false", err)
	}
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiError.StatusCode != http.StatusNotFound {
		t.Errorf("status code = %d", apiError.StatusCode)
	}
	if !strings.Contains(apiError.Error(), `"message":"missing"`) {
		t.Errorf("error = %q", apiError)
	}
}
