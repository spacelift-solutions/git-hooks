package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		wantOK       bool
		wantRepo     string
		wantDryRun   bool
		wantRulesets bool
	}{
		{
			name:     "repository",
			args:     []string{"acme/project"},
			wantOK:   true,
			wantRepo: "acme/project",
		},
		{
			name:         "both flags",
			args:         []string{"--dry-run", "--rulesets-only", "acme/project"},
			wantOK:       true,
			wantRepo:     "acme/project",
			wantDryRun:   true,
			wantRulesets: true,
		},
		{name: "missing repository"},
		{name: "unknown flag", args: []string{"--unknown", "acme/project"}},
		{name: "flag after repository", args: []string{"acme/project", "--dry-run"}},
		{name: "missing owner", args: []string{"/project"}},
		{name: "missing repository name", args: []string{"acme/"}},
		{name: "extra path component", args: []string{"acme/group/project"}},
		{name: "extra argument", args: []string{"acme/project", "other/project"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options, ok := parseArgs(test.args)
			if ok != test.wantOK {
				t.Fatalf("parseArgs() ok = %t, want %t", ok, test.wantOK)
			}
			if !ok {
				return
			}
			if options.Repository != test.wantRepo ||
				options.DryRun != test.wantDryRun ||
				options.RulesetsOnly != test.wantRulesets {
				t.Errorf("parseArgs() = %#v", options)
			}
		})
	}
}

func TestRunReportsUsageError(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"bad"},
		func(string) string { return "" },
		&stdout,
		&stderr,
	)
	if exitCode != 2 {
		t.Errorf("run() exit code = %d, want 2", exitCode)
	}
	if stderr.String() != usageText+"\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunReportsAuthenticationError(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"acme/project"},
		func(string) string { return "" },
		&bytes.Buffer{},
		&stderr,
	)
	if exitCode != 1 {
		t.Errorf("run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "authentication required") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunUsesConfiguredAPIAndToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/repos/acme/project":
			writeCommandJSON(t, writer, map[string]any{"default_branch": "main"})
		case "/orgs/acme/teams/the-four-ghostman":
			writeCommandJSON(t, writer, map[string]any{"id": 77})
		case "/repos/acme/project/rulesets":
			if request.Method == http.MethodGet {
				writeCommandJSON(t, writer, []any{})
				return
			}
			var payload struct {
				BypassActors []struct {
					ActorID   int64  `json:"actor_id"`
					ActorType string `json:"actor_type"`
				} `json:"bypass_actors"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode ruleset payload: %v", err)
			}
			if len(payload.BypassActors) != 2 ||
				payload.BypassActors[1].ActorID != 1234 ||
				payload.BypassActors[1].ActorType != "Integration" {
				t.Errorf("bypass actors = %#v", payload.BypassActors)
			}
			writer.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.Error(writer, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	environment := map[string]string{
		"GH_TOKEN":             "test-token",
		"GITHUB_API_URL":       server.URL,
		"TF_VAR_github_app_id": "1234",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--rulesets-only", "acme/project"},
		func(name string) string { return environment[name] },
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Created Require The Four Ghostman review") ||
		!strings.Contains(stdout.String(), "Created Require Spacelift repository checks") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunRejectsInvalidGitHubAppID(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--rulesets-only", "acme/project"},
		func(name string) string {
			if name == githubAppIDEnv {
				return "not-an-id"
			}
			return ""
		},
		&bytes.Buffer{},
		&stderr,
	)
	if exitCode != 1 {
		t.Errorf("run() exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), githubAppIDEnv+" must be a positive integer") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func writeCommandJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
