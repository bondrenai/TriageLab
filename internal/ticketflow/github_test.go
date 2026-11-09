package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type githubFixture struct {
	source   *githubSource
	issue    map[string]any
	comments []map[string]any
	link     string
	calls    int
	before   func(*http.Request)
}

func newGitHubFixture(t *testing.T) *githubFixture {
	t.Helper()
	f := &githubFixture{
		source: newGitHub("owner/repo", "test-github-secret"),
		issue: map[string]any{
			"id": 500, "number": 42, "title": "Timeout", "body": "Python version: 3.12.4\nExpected: five seconds\nActual: five milliseconds\n[app.py.txt](https://github.com/user-attachments/files/60/app.py.txt)",
			"state": "open", "user": map[string]any{"id": 10, "login": "reporter"},
			"updated_at": "2026-11-09T12:00:00Z", "comments": 2,
			"labels": []map[string]string{{"name": "reproduce"}},
		},
		comments: []map[string]any{
			{"id": 20, "body": "Expected: an unrelated person's request", "user": map[string]any{"id": 11, "login": "someone-else"}, "updated_at": "2026-11-09T12:01:00Z"},
			{"id": 21, "body": "Actual: corrected description", "user": map[string]any{"id": 10, "login": "reporter"}, "updated_at": "2026-11-09T12:02:00Z"},
		},
	}
	f.source.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
		f.calls++
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" ||
			r.Header.Get("Authorization") != "Bearer test-github-secret" || r.Header.Get("X-GitHub-Api-Version") != githubAPIVersion ||
			r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") != "TriageLab" {
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.Redacted())
		}
		if f.before != nil {
			f.before(r)
		}
		var value any
		switch r.URL.Path {
		case "/repos/owner/repo/issues/42":
			if r.URL.RawQuery != "" {
				t.Fatal("unexpected issue query")
			}
			value = f.issue
		case "/repos/owner/repo/issues/42/comments":
			if r.URL.RawQuery != "per_page=100&page=1" {
				t.Fatal("comment request was not bounded")
			}
			value = f.comments
		default:
			t.Fatalf("unexpected API path %s", r.URL.Path)
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		res := githubResponse(200, string(body))
		res.Header.Set("Link", f.link)
		return res, nil
	})
	return f
}

func TestGitHubReadsIssueBodyCommentsAndProvenance(t *testing.T) {
	f := newGitHubFixture(t)
	s, err := f.source.read(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 4 || s.Ticket.ID != 42 || s.Ticket.Kind != "github" || s.Ticket.URL != "https://github.com/owner/repo/issues/42" ||
		s.Ticket.RequesterProfile.URL != "https://github.com/reporter" || s.Ticket.Requester != 10 || s.Ticket.Tags[0] != "reproduce" ||
		len(s.Comments) != 3 || s.Comments[0].ID != 500 || s.Comments[0].URL != s.Ticket.URL ||
		s.Comments[1].URL != s.Ticket.URL+"#issuecomment-20" || s.Comments[1].Author != 11 || s.Comments[2].Author != 10 {
		t.Fatalf("incorrect issue snapshot: %+v", s)
	}
	files := s.Comments[0].Files
	if len(files) != 1 || files[0].Name != "app.py.txt" || files[0].Size != -1 || s.Ticket.Updated == "" {
		t.Fatalf("missing attachment or freshness receipt: %+v", s)
	}
	if _, err := f.source.postComment(t.Context(), s.Ticket, "do not send", true); !errors.Is(err, ErrBlocked) || f.calls != 4 {
		t.Fatalf("read-only source attempted a write: %v", err)
	}
}

func TestGitHubConfirmDetectsCommentEditsAndDeletions(t *testing.T) {
	for _, change := range []string{"body", "updated", "deleted", "author", "issue body", "label"} {
		t.Run(change, func(t *testing.T) {
			f := newGitHubFixture(t)
			s, err := f.source.read(t.Context(), 42)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				f.comments[1]["body"] = "An edited comment with the same issue timestamp"
			case "updated":
				f.comments[1]["updated_at"] = "2026-11-09T12:03:00Z"
			case "deleted":
				f.comments = f.comments[:1]
				f.issue["comments"] = 1
			case "author":
				f.comments[1]["user"] = map[string]any{"id": 12, "login": "different"}
			case "issue body":
				f.issue["body"] = "Changed issue body"
			case "label":
				f.issue["labels"] = []map[string]string{{"name": "different"}}
			}
			if err := f.source.confirm(t.Context(), s.Ticket); !errors.Is(err, ErrChanged) {
				t.Fatalf("failed to detect %s change: %v", change, err)
			}
		})
	}
}

func TestGitHubRejectsChangingSnapshot(t *testing.T) {
	f := newGitHubFixture(t)
	f.before = func(r *http.Request) {
		if f.calls == 4 {
			f.comments[0]["body"] = "Edited during the read"
		}
	}
	if _, err := f.source.read(t.Context(), 42); !errors.Is(err, ErrChanged) {
		t.Fatalf("accepted mixed snapshot: %v", err)
	}
}

func TestGitHubRejectsInvalidOrIncompleteIssues(t *testing.T) {
	for _, invalid := range []string{"pull request", "wrong number", "missing author", "missing count", "invalid time", "too many", "next page", "count mismatch", "null comments", "duplicate comment", "unordered comments", "invalid comment"} {
		t.Run(invalid, func(t *testing.T) {
			f := newGitHubFixture(t)
			switch invalid {
			case "pull request":
				f.issue["pull_request"] = map[string]string{"url": "https://api.github.com/repos/owner/repo/pulls/42"}
			case "wrong number":
				f.issue["number"] = 43
			case "missing author":
				delete(f.issue, "user")
			case "missing count":
				delete(f.issue, "comments")
			case "invalid time":
				f.issue["updated_at"] = "invalid"
			case "too many":
				f.issue["comments"] = 101
			case "next page":
				f.link = `<https://untrusted.example/secret>; rel="next"`
			case "count mismatch":
				f.issue["comments"] = 3
			case "null comments":
				f.comments = nil
				f.issue["comments"] = 0
			case "duplicate comment":
				f.comments[1]["id"] = 20
			case "unordered comments":
				f.comments[1]["id"] = 19
			case "invalid comment":
				f.comments[1]["updated_at"] = "invalid"
			}
			if _, err := f.source.read(t.Context(), 42); err == nil {
				t.Fatal("accepted invalid GitHub snapshot")
			}
		})
	}
	f := newGitHubFixture(t)
	if _, err := f.source.read(t.Context(), 0); err == nil || f.calls != 0 {
		t.Fatal("invalid issue number reached the API")
	}
	f.issue["comments"] = 0
	f.comments = []map[string]any{}
	if s, err := f.source.read(t.Context(), 42); err != nil || len(s.Comments) != 1 {
		t.Fatalf("an issue without comments should include its body: %+v %v", s, err)
	}
}

func TestGitHubAPIFailsWithoutLeakingProviderContentOrFollowingRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			g := newGitHub("owner/repo", "test-github-secret")
			calls := 0
			g.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "api.github.com" {
					t.Fatal("followed an API redirect")
				}
				res := githubResponse(status, "provider body with test-github-secret")
				res.Header.Set("Location", "https://untrusted.example/token")
				return res, nil
			})
			_, err := g.read(t.Context(), 42)
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "untrusted") || calls != 1 {
				t.Fatalf("unsafe API error or redirect: %v (%d calls)", err, calls)
			}
		})
	}
	g := newGitHub("owner/repo", "")
	g.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("anonymous API read sent authorization")
		}
		return githubResponse(200, strings.Repeat("x", maxProviderBytes+1)), nil
	})
	if _, err := g.read(t.Context(), 42); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized response was accepted: %v", err)
	}
}

func TestGitHubAttachmentLinksAreStrictAndKeepServerFilenames(t *testing.T) {
	body := strings.Join([]string{
		"[renamed.py](https://github.com/user-attachments/files/60/app.py.txt)",
		"[duplicate](https://github.com/user-attachments/files/60/app.py.txt)",
		"[config.json](https://github.com/user-attachments/files/61/config.json)",
		"[no](https://github.com.evil.test/user-attachments/files/60/app.py)",
		"[no](https://user@github.com/user-attachments/files/60/app.py)",
		"[no](https://github.com:443/user-attachments/files/60/app.py)",
		"[no](https://github.com/user-attachments/files/60/app.py?secret=1)",
		"[no](https://github.com/user-attachments/files/60/%2e%2e%2fapp.py)",
		"[no](https://github.com/user-attachments/files/60/../app.py)",
		"[no](http://github.com/user-attachments/files/60/app.py)",
		"[no](https://raw.githubusercontent.com/owner/repo/main/app.py)",
	}, "\n")
	files := githubAttachments(body)
	if len(files) != 2 || files[0].Name != "app.py.txt" || files[1].Name != "config.json" {
		t.Fatalf("unsafe or mislabeled attachments: %+v", files)
	}
}

func TestGitHubAttachmentDownloadBoundsAndRedirects(t *testing.T) {
	const initial = "https://github.com/user-attachments/files/60/app.py.txt"
	const signed = "https://objects.githubusercontent.com/github-production-repository-file-5c1aeb/123/60?signature=private"
	for _, tc := range []struct {
		name, target, body string
		status, calls      int
		good               bool
	}{
		{"direct", "", "print(1)\n", 200, 1, true},
		{"signed download", signed, "print(1)\n", 200, 2, true},
		{"external redirect", "https://untrusted.example/app.py", "", 200, 1, false},
		{"HTTP redirect", strings.Replace(signed, "https:", "http:", 1), "", 200, 1, false},
		{"credential redirect", strings.Replace(signed, "https://", "https://user@", 1), "", 200, 1, false},
		{"foreign file", strings.Replace(signed, "/60?", "/61?", 1), "", 200, 1, false},
		{"foreign path", "https://objects.githubusercontent.com/something/60", "", 200, 1, false},
		{"second redirect", signed, "", 302, 2, false},
		{"private attachment", "", "private contents", 404, 1, false},
		{"empty file", "", "", 200, 1, false},
		{"binary file", "", "print(1)\x00", 200, 1, false},
		{"invalid UTF-8", "", "\xff", 200, 1, false},
		{"too large", "", strings.Repeat("x", casefile.MaxFileBytes+1), 200, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitHub("owner/repo", "test-github-secret")
			calls := 0
			g.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || len(r.Header) != 0 {
					t.Fatal("attachment download sent credentials or API headers")
				}
				if calls == 1 {
					if r.URL.String() != initial {
						t.Fatal("unexpected initial attachment request")
					}
					if tc.target != "" {
						res := githubResponse(302, "")
						res.Header.Set("Location", tc.target)
						return res, nil
					}
				} else if calls != 2 || r.URL.String() != signed {
					t.Fatal("followed an unapproved attachment redirect")
				}
				res := githubResponse(tc.status, tc.body)
				res.Header.Set("Location", "https://untrusted.example/second")
				return res, nil
			})
			data, err := g.file(t.Context(), attachment{Name: "app.py.txt", URL: initial, Size: -1})
			if (err == nil) != tc.good || calls != tc.calls || (tc.good && string(data) != tc.body) ||
				(err != nil && (strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret"))) {
				t.Fatalf("unexpected download outcome: %v, %d calls", err, calls)
			}
		})
	}
}

func TestGitHubAttachmentRejectsUnsafeInitialLocationBeforeRequest(t *testing.T) {
	g := newGitHub("owner/repo", "secret")
	g.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsafe attachment reached the network")
		return nil, errors.New("unexpected request")
	})
	for _, target := range []string{
		"http://github.com/user-attachments/files/60/app.py",
		"https://github.com.evil.test/user-attachments/files/60/app.py",
		"https://github.com:443/user-attachments/files/60/app.py",
		"https://user@github.com/user-attachments/files/60/app.py",
		"https://github.com/user-attachments/files/60/app.py?token=secret",
		"https://github.com/user-attachments/files/60/app.py#fragment",
		"https://github.com/user-attachments/files/60/%61pp.py",
		"https://github.com/user-attachments/files/60/other.py",
		"https://github.com/user-attachments/files/60/archive.zip",
	} {
		if _, err := g.file(t.Context(), attachment{Name: "app.py", URL: target, Size: -1}); err == nil {
			t.Fatalf("accepted unsafe attachment %s", target)
		}
	}
}

func githubWriter(t *testing.T, post func(*http.Request) (*http.Response, error)) (*githubFixture, ticket) {
	t.Helper()
	f := newGitHubFixture(t)
	s, err := f.source.read(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	f.source.writable = true
	read := f.source.client.Transport
	f.source.client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return read.RoundTrip(r)
		}
		if r.Method != http.MethodPost || r.URL.String() != "https://api.github.com/repos/owner/repo/issues/42/comments" ||
			r.Header.Get("Authorization") != "Bearer test-github-secret" || r.Header.Get("Accept") != "application/vnd.github+json" ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-GitHub-Api-Version") != githubAPIVersion ||
			r.Header.Get("User-Agent") != "TriageLab" || r.Header.Get("Idempotency-Key") != "" || r.GetBody != nil {
			t.Fatal("unexpected or replayable GitHub comment request")
		}
		if f.calls != 6 {
			t.Fatal("comment write did not immediately follow a complete freshness check")
		}
		return post(r)
	})
	return f, s.Ticket
}

func githubCommentReceipt(t *testing.T, id int64, message string) *http.Response {
	t.Helper()
	// Repository and account names are case-insensitive on GitHub.
	data, err := json.Marshal(map[string]any{
		"id": id, "body": message,
		"issue_url": "https://api.github.com/repos/Owner/Repo/issues/42",
		"html_url":  "https://github.com/Owner/Repo/issues/42#issuecomment-" + strconv.FormatInt(id, 10),
	})
	if err != nil {
		t.Fatal(err)
	}
	return githubResponse(http.StatusCreated, string(data))
}

func TestGitHubPostCommentVerifiesReceiptAndSendsExactlyOnce(t *testing.T) {
	for _, message := range []string{"Reviewed clarification\nPlease attach the reproduction.", strings.Repeat("x", maxGitHubCommentBytes)} {
		posts := 0
		const receiptID int64 = 9007199254740993
		f, reviewed := githubWriter(t, func(r *http.Request) (*http.Response, error) {
			posts++
			data, err := io.ReadAll(r.Body)
			if err != nil || int64(len(data)) != r.ContentLength {
				t.Fatal("comment request body was incomplete")
			}
			var submitted map[string]string
			if json.Unmarshal(data, &submitted) != nil || len(submitted) != 1 || submitted["body"] != message {
				t.Fatal("comment body differs from reviewed text")
			}
			return githubCommentReceipt(t, receiptID, message), nil
		})
		id, err := f.source.postComment(t.Context(), reviewed, message, true)
		if err != nil || id != receiptID || posts != 1 {
			t.Fatalf("comment was not confirmed exactly once: id %d, posts %d, error %v", id, posts, err)
		}
	}
}

func TestGitHubPostCommentRejectsStaleSnapshotWithoutWriting(t *testing.T) {
	f, reviewed := githubWriter(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("stale snapshot caused a comment write")
		return nil, errors.New("unexpected write")
	})
	f.comments[1]["body"] = "Requester edited the last reply"
	if _, err := f.source.postComment(t.Context(), reviewed, "Reviewed clarification", true); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale comment was not rejected: %v", err)
	}
}

func TestGitHubPostCommentRequiresWriteCapabilityAndPublicVisibility(t *testing.T) {
	for _, invalid := range []string{"private", "disabled", "no token", "wrong source", "wrong account", "closed", "invalid number"} {
		t.Run(invalid, func(t *testing.T) {
			f, reviewed := githubWriter(t, nil)
			public := true
			switch invalid {
			case "private":
				public = false
			case "disabled":
				f.source.writable = false
			case "no token":
				f.source.token = ""
			case "wrong source":
				reviewed.Kind = "zendesk"
			case "wrong account":
				reviewed.URL = "https://github.com/another/repo/issues/42"
			case "closed":
				reviewed.Status = "closed"
			case "invalid number":
				reviewed.ID = 0
			}
			f.source.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("blocked write contacted GitHub")
				return nil, errors.New("unexpected request")
			})
			if _, err := f.source.postComment(t.Context(), reviewed, "Reviewed clarification", public); !errors.Is(err, ErrBlocked) {
				t.Fatalf("write was not blocked: %v", err)
			}
		})
	}
}

func TestGitHubPostCommentRejectsUnboundedOrInvalidBodies(t *testing.T) {
	for _, message := range []string{"", " \n\t ", strings.Repeat("x", maxGitHubCommentBytes+1), "\xff", "text\x00"} {
		f, reviewed := githubWriter(t, nil)
		f.source.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid comment body reached GitHub")
			return nil, errors.New("unexpected request")
		})
		if _, err := f.source.postComment(t.Context(), reviewed, message, true); err == nil {
			t.Fatal("accepted an invalid comment body")
		}
	}
}

func TestGitHubPostCommentDoesNotRetryOrFollowRedirects(t *testing.T) {
	for _, status := range []int{200, 301, 302, 307, 308, 401, 403, 404, 422, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			posts := 0
			f, reviewed := githubWriter(t, func(*http.Request) (*http.Response, error) {
				posts++
				res := githubResponse(status, "provider response contains test-github-secret")
				res.Header.Set("Location", "https://untrusted.example/comments")
				return res, nil
			})
			_, err := f.source.postComment(t.Context(), reviewed, "Reviewed clarification", true)
			if err == nil || errors.Is(err, ErrChanged) || errors.Is(err, ErrBlocked) || posts != 1 ||
				strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "untrusted") {
				t.Fatalf("unsafe delivery outcome: %v, %d posts", err, posts)
			}
		})
	}
	posts := 0
	f, reviewed := githubWriter(t, func(*http.Request) (*http.Response, error) {
		posts++
		return nil, errors.New("uncertain response contains test-github-secret")
	})
	_, err := f.source.postComment(t.Context(), reviewed, "Reviewed clarification", true)
	if err == nil || posts != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("transport failure was retried or leaked: %v, %d posts", err, posts)
	}
}

func TestGitHubPostCommentRejectsMissingOrMismatchedReceipts(t *testing.T) {
	for _, invalid := range []string{"malformed", "missing ID", "fractional ID", "overflow ID", "wrong body", "wrong issue", "wrong comment", "foreign URL", "too large"} {
		t.Run(invalid, func(t *testing.T) {
			posts := 0
			f, reviewed := githubWriter(t, func(*http.Request) (*http.Response, error) {
				posts++
				body := `{"id":123,"body":"Reviewed clarification","issue_url":"https://api.github.com/repos/owner/repo/issues/42","html_url":"https://github.com/owner/repo/issues/42#issuecomment-123"}`
				switch invalid {
				case "malformed":
					body = "provider receipt contains test-github-secret"
				case "missing ID":
					body = strings.Replace(body, `"id":123`, `"id":0`, 1)
				case "fractional ID":
					body = strings.Replace(body, `"id":123`, `"id":123.1`, 1)
				case "overflow ID":
					body = strings.Replace(body, `"id":123`, `"id":9223372036854775808`, 1)
				case "wrong body":
					body = strings.Replace(body, "Reviewed clarification", "Different message", 1)
				case "wrong issue":
					body = strings.Replace(body, "issues/42", "issues/43", 1)
				case "wrong comment":
					body = strings.Replace(body, "issuecomment-123", "issuecomment-124", 1)
				case "foreign URL":
					body = strings.Replace(body, "https://github.com/", "https://github.com.evil.test/", 1)
				case "too large":
					body = strings.Repeat("x", maxProviderBytes+1)
				}
				return githubResponse(http.StatusCreated, body), nil
			})
			_, err := f.source.postComment(t.Context(), reviewed, "Reviewed clarification", true)
			if err == nil || errors.Is(err, ErrChanged) || errors.Is(err, ErrBlocked) || posts != 1 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid receipt was accepted or retried: %v, %d posts", err, posts)
			}
		})
	}
}

func TestGitHubPostCommentHonorsCancellationAndTimeout(t *testing.T) {
	t.Run("before write", func(t *testing.T) {
		f, reviewed := githubWriter(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("canceled confirmation caused a write")
			return nil, errors.New("unexpected write")
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.before = func(*http.Request) {
			if f.calls == 6 {
				cancel()
			}
		}
		if _, err := f.source.postComment(ctx, reviewed, "Reviewed clarification", true); !errors.Is(err, context.Canceled) {
			t.Fatalf("ignored cancellation before sending: %v", err)
		}
	})
	t.Run("uncertain write", func(t *testing.T) {
		posts := 0
		f, reviewed := githubWriter(t, func(r *http.Request) (*http.Response, error) {
			posts++
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		_, err := f.source.postComment(ctx, reviewed, "Reviewed clarification", true)
		if err == nil || posts != 1 || !errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, ErrChanged) {
			t.Fatalf("timed out comment was retried or confirmed: %v, %d posts", err, posts)
		}
	})
}
