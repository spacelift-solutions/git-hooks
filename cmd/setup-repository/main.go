package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spacelift-solutions/git-hooks/internal/githubapi"
	"github.com/spacelift-solutions/git-hooks/internal/setuprepo"
)

const usageText = "usage: setup-repository [--dry-run] [--rulesets-only] <owner/repository>"
const githubAppIDEnv = "TF_VAR_github_app_id"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	options, ok := parseArgs(args)
	if !ok {
		fmt.Fprintln(stderr, usageText)
		return 2
	}

	options.BypassAppID = setuprepo.BypassAppID
	if configuredAppID := strings.TrimSpace(getenv(githubAppIDEnv)); configuredAppID != "" {
		appID, err := strconv.ParseInt(configuredAppID, 10, 64)
		if err != nil || appID <= 0 {
			fmt.Fprintf(stderr, "%s must be a positive integer\n", githubAppIDEnv)
			return 1
		}
		options.BypassAppID = appID
	}

	apiURL := getenv("GITHUB_API_URL")
	httpClient := &http.Client{Timeout: 30 * time.Second}
	token, err := githubapi.ResolveToken(ctx, githubapi.AuthOptions{
		APIURL:     apiURL,
		HTTPClient: httpClient,
		Getenv:     getenv,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	runner := setuprepo.Runner{
		Client: githubapi.NewClient(apiURL, token, httpClient),
		Output: stdout,
	}
	if err := runner.Run(ctx, options); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseArgs(args []string) (setuprepo.Options, bool) {
	var options setuprepo.Options
	for len(args) > 0 && len(args[0]) >= 2 && args[0][:2] == "--" {
		switch args[0] {
		case "--dry-run":
			options.DryRun = true
		case "--rulesets-only":
			options.RulesetsOnly = true
		default:
			return setuprepo.Options{}, false
		}
		args = args[1:]
	}

	if len(args) != 1 {
		return setuprepo.Options{}, false
	}
	owner, repository, found := strings.Cut(args[0], "/")
	if !found || owner == "" || repository == "" || strings.Contains(repository, "/") {
		return setuprepo.Options{}, false
	}
	options.Repository = owner + "/" + repository
	return options, true
}
