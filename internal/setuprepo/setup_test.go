package setuprepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacelift-solutions/git-hooks/internal/githubapi"
)

func TestRunCreatesRulesetsBeforeWritingWorkflowToMain(t *testing.T) {
	t.Parallel()

	var createdRulesets []map[string]any
	var workflowWritten bool
	var rulesetsConfigured bool
	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/rulesets":
			writeJSON(t, writer, []any{})
		case "POST /repos/acme/project/rulesets":
			var payload map[string]any
			readJSON(t, request, &payload)
			createdRulesets = append(createdRulesets, payload)
			rulesetsConfigured = len(createdRulesets) == 2
			writer.WriteHeader(http.StatusCreated)
		case "GET /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			http.NotFound(writer, request)
		case "PUT /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			if !rulesetsConfigured {
				t.Error("workflow was written before rulesets were configured")
			}
			var payload struct {
				Message string `json:"message"`
				Content string `json:"content"`
				Branch  string `json:"branch"`
				SHA     string `json:"sha"`
			}
			readJSON(t, request, &payload)
			decoded, err := base64.StdEncoding.DecodeString(payload.Content)
			if err != nil {
				t.Errorf("decode workflow: %v", err)
			}
			if string(decoded) != Workflow {
				t.Errorf("written workflow differs:\n%s", decoded)
			}
			if payload.Message != "Configure Spacelift repository checks" ||
				payload.Branch != "main" ||
				payload.SHA != "" {
				t.Errorf("workflow payload = %#v", payload)
			}
			workflowWritten = true
			writer.WriteHeader(http.StatusCreated)
		default:
			unexpectedRequest(t, writer, request)
		}
	}

	output, err := runWithServer(t, handler, Options{Repository: "acme/project"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !workflowWritten {
		t.Fatal("workflow was not written")
	}
	if len(createdRulesets) != 2 {
		t.Fatalf("created %d rulesets, want 2", len(createdRulesets))
	}
	if createdRulesets[0]["name"] != ReviewRulesetName ||
		createdRulesets[1]["name"] != CheckRulesetName {
		t.Errorf("rulesets created in unexpected order: %#v", createdRulesets)
	}
	for _, expected := range []string{
		"Created " + ReviewRulesetName + " in acme/project.",
		"Created " + CheckRulesetName + " in acme/project.",
		"Configured " + WorkflowPath + " on main in acme/project.",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestRunUpdatesManagedWorkflowAndRepositoryRuleset(t *testing.T) {
	t.Parallel()

	var updatedWorkflowSHA string
	var updatedWorkflowBranch string
	var updatedRuleset bool
	var createdCheckRuleset bool
	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			if request.Header.Get("Accept") == "application/vnd.github.raw+json" {
				_, _ = io.WriteString(writer, "# Managed by spacelift-solutions/git-hooks.\nold\n")
				return
			}
			writeJSON(t, writer, map[string]any{"sha": "old-workflow-sha"})
		case "PUT /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			var payload struct {
				SHA    string `json:"sha"`
				Branch string `json:"branch"`
			}
			readJSON(t, request, &payload)
			updatedWorkflowSHA = payload.SHA
			updatedWorkflowBranch = payload.Branch
			writer.WriteHeader(http.StatusOK)
		case "GET /repos/acme/project/rulesets":
			writeJSON(t, writer, []map[string]any{
				{"id": 101, "name": ReviewRulesetName, "source_type": "Repository"},
				{"id": 202, "name": CheckRulesetName, "source_type": "Organization"},
			})
		case "PUT /repos/acme/project/rulesets/101":
			updatedRuleset = true
			writer.WriteHeader(http.StatusOK)
		case "POST /repos/acme/project/rulesets":
			var payload struct {
				Name string `json:"name"`
			}
			readJSON(t, request, &payload)
			if payload.Name != CheckRulesetName {
				t.Errorf("created ruleset = %q", payload.Name)
			}
			createdCheckRuleset = true
			writer.WriteHeader(http.StatusCreated)
		default:
			unexpectedRequest(t, writer, request)
		}
	}

	output, err := runWithServer(t, handler, Options{Repository: "acme/project"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if updatedWorkflowSHA != "old-workflow-sha" {
		t.Errorf("workflow SHA = %q, want old-workflow-sha", updatedWorkflowSHA)
	}
	if updatedWorkflowBranch != "main" {
		t.Errorf("workflow branch = %q, want main", updatedWorkflowBranch)
	}
	if !updatedRuleset || !createdCheckRuleset {
		t.Errorf(
			"ruleset operations: updated review=%t, created check=%t",
			updatedRuleset,
			createdCheckRuleset,
		)
	}
	if !strings.Contains(output, "Configured "+WorkflowPath+" on main in acme/project.") {
		t.Errorf("output = %q", output)
	}
}

func TestRunLeavesCurrentWorkflowUntouched(t *testing.T) {
	t.Parallel()

	var workflowMutation bool
	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			// The shell implementation ignored trailing newlines through command
			// substitution, so a missing final newline is still current.
			_, _ = io.WriteString(writer, strings.TrimSuffix(Workflow, "\n"))
		case "GET /repos/acme/project/rulesets":
			writeJSON(t, writer, []map[string]any{
				{"id": 1, "name": ReviewRulesetName, "source_type": "Repository"},
				{"id": 2, "name": CheckRulesetName, "source_type": "Repository"},
			})
		case "PUT /repos/acme/project/rulesets/1",
			"PUT /repos/acme/project/rulesets/2":
			writer.WriteHeader(http.StatusOK)
		default:
			if strings.Contains(request.URL.Path, "/contents/") ||
				strings.Contains(request.URL.Path, "/git/") ||
				strings.HasSuffix(request.URL.Path, "/pulls") {
				workflowMutation = true
			}
			unexpectedRequest(t, writer, request)
		}
	}

	output, err := runWithServer(t, handler, Options{Repository: "acme/project"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if workflowMutation {
		t.Error("current workflow was mutated")
	}
	if !strings.Contains(output, "Workflow is already current in acme/project.") {
		t.Errorf("output = %q", output)
	}
}

func TestRunRefusesUnmanagedWorkflow(t *testing.T) {
	t.Parallel()

	var mutation bool
	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			_, _ = io.WriteString(writer, "name: Hand-written workflow\n")
		default:
			mutation = true
			unexpectedRequest(t, writer, request)
		}
	}

	_, err := runWithServer(t, handler, Options{Repository: "acme/project"})
	if err == nil || err.Error() != "refusing to replace unmanaged workflow: "+WorkflowPath {
		t.Fatalf("Run() error = %v", err)
	}
	if mutation {
		t.Error("request was made after detecting an unmanaged workflow")
	}
}

func TestRunDryRunMakesNoMutatingRequests(t *testing.T) {
	t.Parallel()

	var mutation bool
	handler := func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutation = true
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/contents/.github/workflows/spacelift-repository-checks.yml":
			http.NotFound(writer, request)
		case "GET /repos/acme/project/rulesets":
			writeJSON(t, writer, []map[string]any{
				{"id": 9, "name": ReviewRulesetName, "source_type": "Repository"},
			})
		default:
			unexpectedRequest(t, writer, request)
		}
	}

	output, err := runWithServer(t, handler, Options{
		Repository: "acme/project",
		DryRun:     true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if mutation {
		t.Error("dry run made a mutating request")
	}
	for _, expected := range []string{
		"Would create or update " + WorkflowPath + " on main",
		"Would update ruleset 9 in acme/project:",
		"Would create ruleset in acme/project:",
		`"name": "` + ReviewRulesetName + `"`,
		`"name": "` + CheckRulesetName + `"`,
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("output does not contain %q:\n%s", expected, output)
		}
	}
}

func TestRunRulesetsOnlySkipsWorkflow(t *testing.T) {
	t.Parallel()

	handler := func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /repos/acme/project":
			writeJSON(t, writer, map[string]any{"default_branch": "main"})
		case "GET /orgs/acme/teams/the-four-ghostman":
			writeJSON(t, writer, map[string]any{"id": 77})
		case "GET /repos/acme/project/rulesets":
			writeJSON(t, writer, []map[string]any{
				{"id": 1, "name": ReviewRulesetName, "source_type": "Repository"},
				{"id": 2, "name": CheckRulesetName, "source_type": "Repository"},
			})
		case "PUT /repos/acme/project/rulesets/1",
			"PUT /repos/acme/project/rulesets/2":
			writer.WriteHeader(http.StatusOK)
		default:
			unexpectedRequest(t, writer, request)
		}
	}

	output, err := runWithServer(t, handler, Options{
		Repository:   "acme/project",
		RulesetsOnly: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(output, "Workflow") {
		t.Errorf("rulesets-only output mentions workflow: %q", output)
	}
}

func TestRunRejectsNonMainDefaultBranch(t *testing.T) {
	t.Parallel()

	handler := func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/acme/project" {
			unexpectedRequest(t, writer, request)
			return
		}
		writeJSON(t, writer, map[string]any{"default_branch": "master"})
	}

	_, err := runWithServer(t, handler, Options{Repository: "acme/project"})
	if err == nil || err.Error() != "acme/project uses master as its default branch; expected main" {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRulesetPayloadsMatchManagedPayloads(t *testing.T) {
	t.Parallel()

	payloads := newRulesetPayloads(77, 88)
	if len(payloads) != 2 {
		t.Fatalf("newRulesetPayloads() returned %d payloads", len(payloads))
	}

	expected := []string{
		`{
			"name": "Require The Four Ghostman review",
			"target": "branch",
			"enforcement": "active",
			"bypass_actors": [{
				"actor_id": 77,
				"actor_type": "Team",
				"bypass_mode": "always"
			}, {
				"actor_id": 88,
				"actor_type": "Integration",
				"bypass_mode": "always"
			}],
			"conditions": {
				"ref_name": {
					"include": ["refs/heads/main"],
					"exclude": []
				}
			},
			"rules": [{
				"type": "pull_request",
				"parameters": {
					"required_approving_review_count": 0,
					"dismiss_stale_reviews_on_push": true,
					"required_reviewers": [{
						"minimum_approvals": 1,
						"file_patterns": ["*"],
						"reviewer": {"id": 77, "type": "Team"}
					}],
					"require_code_owner_review": true,
					"dismissal_restriction": {
						"enabled": false,
						"allowed_actors": []
					},
					"require_last_push_approval": false,
					"required_review_thread_resolution": false,
					"require_extra_approval_for_unattributed_changes": true,
					"allowed_merge_methods": ["merge", "squash", "rebase"]
				}
			}]
		}`,
		`{
			"name": "Require Spacelift repository checks",
			"target": "branch",
			"enforcement": "active",
			"bypass_actors": [{
				"actor_id": 77,
				"actor_type": "Team",
				"bypass_mode": "always"
			}, {
				"actor_id": 88,
				"actor_type": "Integration",
				"bypass_mode": "always"
			}],
			"conditions": {
				"ref_name": {
					"include": ["refs/heads/main"],
					"exclude": []
				}
			},
			"rules": [{
				"type": "required_status_checks",
				"parameters": {
					"required_status_checks": [{"context": "Spacelift repository checks"}],
					"strict_required_status_checks_policy": true,
					"do_not_enforce_on_create": false
				}
			}]
		}`,
	}

	for index, payload := range payloads {
		actualJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload %d: %v", index, err)
		}
		assertJSONEqual(t, actualJSON, []byte(expected[index]))
	}
}

func runWithServer(
	t *testing.T,
	handler http.HandlerFunc,
	options Options,
) (string, error) {
	t.Helper()

	server := httptest.NewServer(handler)
	defer server.Close()

	var output bytes.Buffer
	runner := Runner{
		Client: githubapi.NewClient(server.URL, "token", server.Client()),
		Output: &output,
	}
	err := runner.Run(context.Background(), options)
	return output.String(), err
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func readJSON(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	defer request.Body.Close()
	if err := json.NewDecoder(request.Body).Decode(destination); err != nil {
		t.Errorf("decode %s %s body: %v", request.Method, request.URL.Path, err)
	}
}

func unexpectedRequest(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()
	t.Errorf("unexpected request: %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
	http.Error(writer, "unexpected request", http.StatusInternalServerError)
}

func assertJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	var actualValue any
	var expectedValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("decode actual JSON: %v\n%s", err, actual)
	}
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("decode expected JSON: %v\n%s", err, expected)
	}
	actualCanonical, _ := json.Marshal(actualValue)
	expectedCanonical, _ := json.Marshal(expectedValue)
	if !bytes.Equal(actualCanonical, expectedCanonical) {
		t.Errorf("JSON differs:\nactual:   %s\nexpected: %s", actualCanonical, expectedCanonical)
	}
}
