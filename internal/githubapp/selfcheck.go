package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"
)

// ErrCredentialsRejected marks a self-check failure caused by the App
// credentials themselves — a malformed private key, or an app id and key that
// GitHub refuses. Such a failure is permanent: no amount of retrying fixes it,
// so callers should fail startup rather than serve with an App that can never
// ingest.
//
// Failures that are *not* this (DNS, refused connections, timeouts, rate
// limits) are transient and must not take down an otherwise healthy API.
var ErrCredentialsRejected = errors.New("github app credentials rejected")

// AppIdentity is what a successful self-check learned about the authenticated
// App. Logging it at startup turns "which App am I?" into an observable fact
// rather than an assumption about which secret got mounted.
type AppIdentity struct {
	ID   int64
	Slug string
	Name string
}

// SelfCheck authenticates as the App itself and reads back its own identity.
//
// It exists because the App credentials are otherwise exercised nowhere before
// the first ingest job: webhooks prove only that GitHub can reach us, and
// /readyz deliberately checks serving dependencies only (GitHub being down must
// not pull the read API out of rotation). Without this, a mangled private key
// surfaced as retries that failed silently and then blocked the repo
// (INV-0007 F5). Here it surfaces once, at boot, naming the cause.
//
// Unlike NewClient this uses an *app* JWT rather than an installation token, so
// it needs no installation id and can run before any repo is onboarded.
func SelfCheck(ctx context.Context, appID int64, pemKey []byte, apiBase string) (*AppIdentity, error) {
	// NewAppsTransport parses the PEM, so a malformed key fails here rather than
	// at the API call — still a credential fault, and the most common one when
	// moving between secret stores.
	atr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, pemKey)
	if err != nil {
		return nil, fmt.Errorf("%w: build app transport: %w", ErrCredentialsRejected, err)
	}

	opts := []github.ClientOptionsFunc{github.WithTransport(atr)}
	if apiBase != "" && apiBase != defaultAPIBase {
		base := strings.TrimSuffix(apiBase, "/")
		atr.BaseURL = base
		opts = append(opts, github.WithEnterpriseURLs(base, base))
	}

	gh, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("build github client: %w", err)
	}
	return selfCheck(ctx, gh)
}

// selfCheck performs the identity call against an already-built client, so the
// classification of GitHub's answer is testable with a stub transport.
func selfCheck(ctx context.Context, gh *github.Client) (*AppIdentity, error) {
	// An empty slug means "the app this JWT authenticates as" (GET /app).
	app, _, err := gh.Apps.Get(ctx, "")
	if err != nil {
		if isCredentialRejection(err) {
			return nil, fmt.Errorf("%w: %w", ErrCredentialsRejected, err)
		}
		return nil, fmt.Errorf("reach github to authenticate the app: %w", err)
	}
	return &AppIdentity{ID: app.GetID(), Slug: app.GetSlug(), Name: app.GetName()}, nil
}

// transientStatuses are 4xx codes that mean "try again later", not "your
// credentials are wrong". They must never fail startup: a throttled or timed-out
// boot would otherwise crash-loop a deploy whose credentials are perfectly good.
//
// go-github only produces a typed *RateLimitError / *AbuseRateLimitError when
// the response carries the matching headers or documentation_url, so a 429 from
// a proxy, a CDN, or a GHES front end arrives as a plain *ErrorResponse and has
// to be caught by status code.
var transientStatuses = []int{
	http.StatusRequestTimeout,
	http.StatusTooManyRequests,
}

// isCredentialRejection reports whether GitHub refused the credentials, as
// opposed to being unreachable or throttling us.
//
// Rate limiting also arrives as a 4xx, so it is excluded explicitly: throttling
// is transient and must not be mistaken for a bad key.
func isCredentialRejection(err error) bool {
	var rateLimit *github.RateLimitError
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &rateLimit) || errors.As(err, &abuse) {
		return false
	}

	var apiErr *github.ErrorResponse
	if !errors.As(err, &apiErr) || apiErr.Response == nil {
		return false
	}
	code := apiErr.Response.StatusCode
	if slices.Contains(transientStatuses, code) {
		return false
	}
	return code >= http.StatusBadRequest && code < http.StatusInternalServerError
}
