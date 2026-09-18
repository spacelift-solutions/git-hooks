package setuprepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spacelift-solutions/git-hooks/internal/githubapi"
)

const (
	ReviewRulesetName = "Require The Four Ghostman review"
	CheckRulesetName  = "Require Spacelift repository checks"
	ReviewerTeam      = "the-four-ghostman"
	RequiredCheck     = "Spacelift repository checks"
	WorkflowPath      = ".github/workflows/spacelift-repository-checks.yml"
	BypassAppID       = 4991387

	workflowMarker = "Managed by spacelift-solutions/git-hooks."
)

const Workflow = `# Managed by spacelift-solutions/git-hooks.
name: Repository checks

on:
  pull_request:
    branches:
      - main
  push:
    branches:
      - main

permissions:
  contents: read

jobs:
  checks:
    name: Spacelift repository checks
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0

      - uses: spacelift-solutions/git-hooks@v1
`

// Options controls repository setup behavior.
type Options struct {
	Repository   string
	DryRun       bool
	RulesetsOnly bool
	BypassAppID  int64
}

// Runner applies the managed workflow and rulesets through the GitHub API.
type Runner struct {
	Client *githubapi.Client
	Output io.Writer
}

// Run configures one owner/repository.
func (runner *Runner) Run(ctx context.Context, options Options) error {
	owner, repository, err := splitRepository(options.Repository)
	if err != nil {
		return err
	}
	if runner.Client == nil {
		return fmt.Errorf("GitHub client is required")
	}
	output := runner.Output
	if output == nil {
		output = io.Discard
	}

	repositoryPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository)
	var metadata struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := runner.Client.Do(ctx, http.MethodGet, repositoryPath, nil, &metadata); err != nil {
		return fmt.Errorf("read repository metadata: %w", err)
	}
	if metadata.DefaultBranch != "main" {
		return fmt.Errorf(
			"%s uses %s as its default branch; expected main",
			options.Repository,
			metadata.DefaultBranch,
		)
	}

	var team struct {
		ID int64 `json:"id"`
	}
	teamPath := "/orgs/" + url.PathEscape(owner) + "/teams/" + ReviewerTeam
	if err := runner.Client.Do(ctx, http.MethodGet, teamPath, nil, &team); err != nil {
		return fmt.Errorf("resolve team %s: %w", ReviewerTeam, err)
	}

	if !options.RulesetsOnly {
		if err := runner.validateWorkflowOwnership(ctx, repositoryPath); err != nil {
			return err
		}
	}

	bypassAppID := options.BypassAppID
	if bypassAppID == 0 {
		bypassAppID = BypassAppID
	}
	if err := runner.configureRulesets(
		ctx,
		output,
		options.Repository,
		repositoryPath,
		team.ID,
		bypassAppID,
		options.DryRun,
	); err != nil {
		return err
	}

	if !options.RulesetsOnly {
		if err := runner.configureWorkflow(
			ctx,
			output,
			options.Repository,
			repositoryPath,
			options.DryRun,
		); err != nil {
			return err
		}
	}

	return nil
}

func splitRepository(repository string) (string, string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repository must be in owner/repository format")
	}
	return parts[0], parts[1], nil
}

func (runner *Runner) validateWorkflowOwnership(
	ctx context.Context,
	repositoryPath string,
) error {
	contentsPath := repositoryPath + "/contents/" + WorkflowPath
	existingWorkflow, err := runner.Client.GetRaw(ctx, contentsPath+"?ref=main")
	switch {
	case err == nil && !bytes.Contains(existingWorkflow, []byte(workflowMarker)):
		return fmt.Errorf("refusing to replace unmanaged workflow: %s", WorkflowPath)
	case err != nil && !githubapi.IsNotFound(err):
		return fmt.Errorf("read workflow from main: %w", err)
	default:
		return nil
	}
}

func (runner *Runner) configureWorkflow(
	ctx context.Context,
	output io.Writer,
	repository string,
	repositoryPath string,
	dryRun bool,
) error {
	contentsPath := repositoryPath + "/contents/" + WorkflowPath
	existingWorkflow, err := runner.Client.GetRaw(ctx, contentsPath+"?ref=main")
	switch {
	case err == nil && workflowsEqual(existingWorkflow, []byte(Workflow)):
		fmt.Fprintf(output, "Workflow is already current in %s.\n", repository)
		return nil
	case err == nil && !bytes.Contains(existingWorkflow, []byte(workflowMarker)):
		return fmt.Errorf("refusing to replace unmanaged workflow: %s", WorkflowPath)
	case err != nil && !githubapi.IsNotFound(err):
		return fmt.Errorf("read workflow from main: %w", err)
	}

	if dryRun {
		fmt.Fprintf(
			output,
			"Would create or update %s on main in %s.\n",
			WorkflowPath,
			repository,
		)
		return nil
	}

	var existingMetadata struct {
		SHA string `json:"sha"`
	}
	if err == nil {
		if err := runner.Client.Do(ctx, http.MethodGet, contentsPath+"?ref=main", nil, &existingMetadata); err != nil {
			return fmt.Errorf("read workflow metadata from main: %w", err)
		}
	}

	workflowPayload := struct {
		Message string `json:"message"`
		Content string `json:"content"`
		Branch  string `json:"branch"`
		SHA     string `json:"sha,omitempty"`
	}{
		Message: "Configure Spacelift repository checks",
		Content: base64.StdEncoding.EncodeToString([]byte(Workflow)),
		Branch:  "main",
		SHA:     existingMetadata.SHA,
	}
	if err := runner.Client.Do(
		ctx,
		http.MethodPut,
		contentsPath,
		workflowPayload,
		nil,
	); err != nil {
		return fmt.Errorf("write workflow to main: %w", err)
	}

	fmt.Fprintf(output, "Configured %s on main in %s.\n", WorkflowPath, repository)
	return nil
}

// Bash command substitution removed trailing newlines in the original setup
// script, so keep that equivalence when deciding whether a workflow is current.
func workflowsEqual(left, right []byte) bool {
	return bytes.Equal(bytes.TrimRight(left, "\n"), bytes.TrimRight(right, "\n"))
}

type rulesetPayload struct {
	Name         string        `json:"name"`
	Target       string        `json:"target"`
	Enforcement  string        `json:"enforcement"`
	BypassActors []bypassActor `json:"bypass_actors"`
	Conditions   conditions    `json:"conditions"`
	Rules        []rulesetRule `json:"rules"`
}

type bypassActor struct {
	ActorID    int64  `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

type conditions struct {
	ReferenceName referenceNameCondition `json:"ref_name"`
}

type referenceNameCondition struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

type rulesetRule struct {
	Type       string `json:"type"`
	Parameters any    `json:"parameters"`
}

type pullRequestParameters struct {
	RequiredApprovingReviewCount               int                  `json:"required_approving_review_count"`
	DismissStaleReviewsOnPush                  bool                 `json:"dismiss_stale_reviews_on_push"`
	RequiredReviewers                          []requiredReviewer   `json:"required_reviewers"`
	RequireCodeOwnerReview                     bool                 `json:"require_code_owner_review"`
	DismissalRestriction                       dismissalRestriction `json:"dismissal_restriction"`
	RequireLastPushApproval                    bool                 `json:"require_last_push_approval"`
	RequiredReviewThreadResolution             bool                 `json:"required_review_thread_resolution"`
	RequireExtraApprovalForUnattributedChanges bool                 `json:"require_extra_approval_for_unattributed_changes"`
	AllowedMergeMethods                        []string             `json:"allowed_merge_methods"`
}

type requiredReviewer struct {
	MinimumApprovals int      `json:"minimum_approvals"`
	FilePatterns     []string `json:"file_patterns"`
	Reviewer         reviewer `json:"reviewer"`
}

type reviewer struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type dismissalRestriction struct {
	Enabled       bool  `json:"enabled"`
	AllowedActors []any `json:"allowed_actors"`
}

type requiredStatusChecksParameters struct {
	RequiredStatusChecks             []requiredStatusCheck `json:"required_status_checks"`
	StrictRequiredStatusChecksPolicy bool                  `json:"strict_required_status_checks_policy"`
	DoNotEnforceOnCreate             bool                  `json:"do_not_enforce_on_create"`
}

type requiredStatusCheck struct {
	Context string `json:"context"`
}

func newRulesetPayloads(teamID, bypassAppID int64) []rulesetPayload {
	common := func(name string, rules []rulesetRule) rulesetPayload {
		return rulesetPayload{
			Name:        name,
			Target:      "branch",
			Enforcement: "active",
			BypassActors: []bypassActor{
				{
					ActorID:    teamID,
					ActorType:  "Team",
					BypassMode: "always",
				},
				{
					ActorID:    bypassAppID,
					ActorType:  "Integration",
					BypassMode: "always",
				},
			},
			Conditions: conditions{
				ReferenceName: referenceNameCondition{
					Include: []string{"refs/heads/main"},
					Exclude: []string{},
				},
			},
			Rules: rules,
		}
	}

	reviewRuleset := common(ReviewRulesetName, []rulesetRule{
		{
			Type: "pull_request",
			Parameters: pullRequestParameters{
				RequiredApprovingReviewCount: 0,
				DismissStaleReviewsOnPush:    true,
				RequiredReviewers: []requiredReviewer{
					{
						MinimumApprovals: 1,
						FilePatterns:     []string{"*"},
						Reviewer: reviewer{
							ID:   teamID,
							Type: "Team",
						},
					},
				},
				RequireCodeOwnerReview: true,
				DismissalRestriction: dismissalRestriction{
					Enabled:       false,
					AllowedActors: []any{},
				},
				RequireLastPushApproval:                    false,
				RequiredReviewThreadResolution:             false,
				RequireExtraApprovalForUnattributedChanges: true,
				AllowedMergeMethods:                        []string{"merge", "squash", "rebase"},
			},
		},
	})

	checkRuleset := common(CheckRulesetName, []rulesetRule{
		{
			Type: "required_status_checks",
			Parameters: requiredStatusChecksParameters{
				RequiredStatusChecks: []requiredStatusCheck{
					{Context: RequiredCheck},
				},
				StrictRequiredStatusChecksPolicy: true,
				DoNotEnforceOnCreate:             false,
			},
		},
	})

	return []rulesetPayload{reviewRuleset, checkRuleset}
}

func (runner *Runner) configureRulesets(
	ctx context.Context,
	output io.Writer,
	repository string,
	repositoryPath string,
	teamID int64,
	bypassAppID int64,
	dryRun bool,
) error {
	var existingRulesets []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		SourceType string `json:"source_type"`
	}
	if err := runner.Client.Do(
		ctx,
		http.MethodGet,
		repositoryPath+"/rulesets",
		nil,
		&existingRulesets,
	); err != nil {
		return fmt.Errorf("list repository rulesets: %w", err)
	}

	for _, payload := range newRulesetPayloads(teamID, bypassAppID) {
		var rulesetID int64
		for _, existing := range existingRulesets {
			if existing.Name == payload.Name && existing.SourceType == "Repository" {
				rulesetID = existing.ID
				break
			}
		}

		if dryRun {
			if rulesetID != 0 {
				fmt.Fprintf(output, "Would update ruleset %d in %s:\n", rulesetID, repository)
			} else {
				fmt.Fprintf(output, "Would create ruleset in %s:\n", repository)
			}
			encoded, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return fmt.Errorf("encode ruleset %s: %w", payload.Name, err)
			}
			fmt.Fprintln(output, string(encoded))
			continue
		}

		if rulesetID != 0 {
			path := fmt.Sprintf("%s/rulesets/%d", repositoryPath, rulesetID)
			if err := runner.Client.Do(ctx, http.MethodPut, path, payload, nil); err != nil {
				return fmt.Errorf("update ruleset %s: %w", payload.Name, err)
			}
			fmt.Fprintf(output, "Updated %s in %s.\n", payload.Name, repository)
			continue
		}

		if err := runner.Client.Do(
			ctx,
			http.MethodPost,
			repositoryPath+"/rulesets",
			payload,
			nil,
		); err != nil {
			return fmt.Errorf("create ruleset %s: %w", payload.Name, err)
		}
		fmt.Fprintf(output, "Created %s in %s.\n", payload.Name, repository)
	}

	return nil
}
