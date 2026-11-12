package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// VerifySource checks proposed connection settings against the source with
// one bounded, authenticated request. It opens no state directory and holds
// no locks, so it can run before a connection is saved. Zendesk authorizes
// through its browser OAuth flow after saving, so it has nothing to verify
// here; GitHub may authorize anonymously.
func VerifySource(ctx context.Context, config Config) error {
	config = config.withDefaults()
	if err := config.validate(); err != nil {
		return err
	}
	token, err := resolveToken(config)
	if err != nil {
		return err
	}
	switch config.provider() {
	case "github":
		return verifyGitHub(ctx, newGitHub(config.Repository, token))
	case "gitlab":
		return verifyGitLab(ctx, newGitLab(gitLabOrigin(config), config.Repository, token))
	case "linear":
		return verifyLinear(ctx, newLinear(config.Workspace, config.Team, token))
	case "bondren":
		return verifyBondren(ctx, newBondren(config.Origin, token))
	case "jira":
		return verifyJira(ctx, newJira(config.Origin, config.Email, token))
	default:
		return nil
	}
}

// verifyGitHub reads the repository endpoint, which distinguishes a rejected
// token (401) from a missing or invisible repository (404) before any case
// is tracked.
func verifyGitHub(ctx context.Context, g *githubSource) error {
	defer g.client.CloseIdleConnections()
	if _, _, err := g.api(ctx, ""); err != nil {
		return err
	}
	return nil
}

// verifyGitLab reads the project endpoint, which distinguishes a rejected
// token (401) from a missing or inaccessible project (404) before any case
// is tracked. Project payloads are large; the standard provider bound
// applies.
func verifyGitLab(ctx context.Context, g *gitlabSource) error {
	defer g.client.CloseIdleConnections()
	if _, _, err := g.request(ctx, http.MethodGet, "/projects/"+gitlabProjectPath(g.project), nil, maxProviderBytes); err != nil {
		return err
	}
	return nil
}

func verifyLinear(ctx context.Context, l *linearSource) error {
	defer l.client.CloseIdleConnections()
	if _, err := l.graphQL(ctx, linearViewerDocument, nil, false); err != nil {
		if errors.Is(err, ErrOAuthRequired) {
			return errors.New("Linear rejected the API key")
		}
		return err
	}
	return nil
}

// verifyBondren queries a number no issue can carry; a valid token answers
// with an empty page and an invalid one fails the request.
func verifyBondren(ctx context.Context, b *bondrenSource) error {
	defer b.client.CloseIdleConnections()
	query, err := json.Marshal(bondrenIssueQuery{Limit: 1, FilterGroup: bondrenFilterGroup{Op: "and",
		Filters: []bondrenFilter{{Field: "number", Op: "eq", Value: "0"}}}})
	if err != nil {
		return errors.New("invalid Bondren request")
	}
	res, err := b.callResponse(ctx, http.MethodPost, "/api/v1/issues/query", query)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusOK:
		return nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return errors.New("Bondren rejected the agent token")
	default:
		return fmt.Errorf("Bondren returned HTTP %d", res.StatusCode)
	}
}

func verifyJira(ctx context.Context, j *jiraSource) error {
	defer j.client.CloseIdleConnections()
	res, err := j.fetch(ctx, "/rest/api/3/myself")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusOK:
		return nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return errors.New("Jira rejected the Atlassian account email or API token")
	default:
		return fmt.Errorf("Jira returned HTTP %d", res.StatusCode)
	}
}

const linearViewerDocument = `query TriageLabViewer { viewer { id name } }`
