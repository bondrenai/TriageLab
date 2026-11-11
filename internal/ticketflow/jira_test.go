package ticketflow

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

type jiraTransport func(*http.Request) (*http.Response, error)

func (f jiraTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jiraResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func jiraJSON(value any) *http.Response {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return jiraResponse(200, string(body))
}

const (
	jiraTestOrigin    = "https://test-site.atlassian.net"
	jiraTestEmail     = "triagelab@example.com"
	jiraTestToken     = "test-jira-api-token"
	jiraIssueID       = "10042"
	jiraIssueKeyValue = "OPS-42"
	jiraReporterID    = "5b10a2844c20165700ede21g"
	jiraAgentID       = "5b10a2844c20165700ede21h"
	jiraAttachmentID  = "10001"
)

func jiraBasicAuthorization() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(jiraTestEmail+":"+jiraTestToken))
}

// jiraADF builds a single-paragraph ADF document with hardBreaks.
func jiraADF(lines ...string) string {
	var content []map[string]any
	for i, line := range lines {
		if i > 0 {
			content = append(content, map[string]any{"type": "hardBreak"})
		}
		if line != "" {
			content = append(content, map[string]any{"type": "text", "text": line})
		}
	}
	body, err := json.Marshal(map[string]any{"type": "doc", "version": 1,
		"content": []any{map[string]any{"type": "paragraph", "content": content}}})
	if err != nil {
		panic(err)
	}
	return string(body)
}

func jiraIssueJSON() string {
	return `{"id":"` + jiraIssueID + `","key":"` + jiraIssueKeyValue + `","fields":{` +
		`"summary":"Timeout is too short","description":` + jiraADF("Python version: 3.12.4", "Expected: five seconds", "Actual: five milliseconds") + `,` +
		`"updated":"2026-11-11T12:00:00.000+0000",` +
		`"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},` +
		`"labels":["reproduce"],` +
		`"reporter":{"accountId":"` + jiraReporterID + `","displayName":"Reporter","emailAddress":"reporter@example.com"},` +
		`"attachment":[{"id":"` + jiraAttachmentID + `","filename":"app.py","size":17,"mimeType":"text/x-python"}]}}`
}

func jiraCommentsJSON() string {
	return `{"comments":[` +
		`{"id":"10010","self":"` + jiraTestOrigin + `/rest/api/3/issue/` + jiraIssueID + `/comment/10010",` +
		`"author":{"accountId":"` + jiraReporterID + `","displayName":"Reporter"},` +
		`"body":` + jiraADF("The smallest reproduction.") + `,` +
		`"created":"2026-11-11T12:00:10.000+0000","updated":"2026-11-11T12:00:10.000+0000","jsdPublic":true},` +
		`{"id":"10011","self":"` + jiraTestOrigin + `/rest/api/3/issue/` + jiraIssueID + `/comment/10011",` +
		`"author":{"accountId":"` + jiraAgentID + `","displayName":"Agent"},` +
		`"body":` + jiraADF("Taking a look.") + `,` +
		`"created":"2026-11-11T12:01:00.000+0000","updated":"2026-11-11T12:01:00.000+0000","jsdPublic":false},` +
		`{"id":"10012","self":"` + jiraTestOrigin + `/rest/api/3/issue/` + jiraIssueID + `/comment/10012",` +
		`"author":{"accountId":"` + jiraReporterID + `","displayName":"Reporter"},` +
		`"body":` + jiraADF("Internal aside.") + `,` +
		`"created":"2026-11-11T12:02:00.000+0000","updated":"2026-11-11T12:02:00.000+0000",` +
		`"visibility":{"type":"role","value":"Administrators"}}` +
		`],"startAt":0,"maxResults":100,"total":3}`
}

func jiraReceiptJSON(id, body string) string {
	return `{"id":"` + id + `","self":"` + jiraTestOrigin + `/rest/api/3/issue/` + jiraIssueID + `/comment/` + id + `",` +
		`"author":{"accountId":"` + jiraAgentID + `","displayName":"Agent"},` +
		`"body":` + jiraADF(strings.Split(body, "\n")...) + `,` +
		`"created":"2026-11-11T12:05:00.000+0000","updated":"2026-11-11T12:05:00.000+0000"}`
}

type jiraFixture struct {
	source    *jiraSource
	issue     string
	comments  string
	post      string
	postCode  int
	download  string
	mediaCode int
	calls     int
	// postInput records the decoded request the connector sent.
	postInput struct {
		Body       map[string]any
		Properties []struct {
			Key   string `json:"key"`
			Value struct {
				Internal bool `json:"internal"`
			} `json:"value"`
		} `json:"properties"`
	}
	mediaRequests int
}

func newJiraFixture(t *testing.T) *jiraFixture {
	t.Helper()
	f := &jiraFixture{source: newJira(jiraTestOrigin, jiraTestEmail, jiraTestToken),
		issue: jiraIssueJSON(), comments: jiraCommentsJSON(), postCode: 201}
	f.source.client.Transport = jiraTransport(func(r *http.Request) (*http.Response, error) {
		f.calls++
		if strings.HasPrefix(r.URL.Host, "api.media.atlassian.com") {
			f.mediaRequests++
			// The signed media URL is its own grant; credentials must stay home.
			if r.Header.Get("Authorization") != "" {
				t.Fatal("site credential traveled to the Jira media host")
			}
			if r.URL.Scheme != "https" {
				t.Fatalf("media download left HTTPS: %s", r.URL.Redacted())
			}
			if f.mediaCode != 0 {
				return jiraResponse(f.mediaCode, ""), nil
			}
			if f.download == "" {
				f.download = "print('timeout')\n"
			}
			return jiraResponse(200, f.download), nil
		}
		if r.Header.Get("Authorization") != jiraBasicAuthorization() || r.Header.Get("User-Agent") != "TriageLab" {
			t.Fatalf("unexpected Jira authorization: %s %s", r.Method, r.URL.Redacted())
		}
		if r.URL.Scheme+"://"+r.URL.Host != jiraTestOrigin {
			t.Fatalf("Jira request left the configured site: %s", r.URL.Redacted())
		}
		switch {
		case r.URL.Path == "/rest/api/3/issue/"+jiraIssueID || r.URL.Path == "/rest/api/3/issue/"+jiraIssueKeyValue:
			if r.Method != http.MethodGet || r.URL.RawQuery != "fields=summary,description,updated,status,labels,reporter,attachment" {
				t.Fatalf("unexpected issue request: %s %s", r.Method, r.URL.Redacted())
			}
			return jiraResponse(200, f.issue), nil
		case r.URL.Path == "/rest/api/3/issue/"+jiraIssueID+"/comment":
			if r.Method == http.MethodGet {
				if r.URL.RawQuery != "maxResults=100&orderBy=created" {
					t.Fatalf("unexpected comment page query: %s", r.URL.Redacted())
				}
				return jiraResponse(200, f.comments), nil
			}
			if f.post == "" {
				t.Fatal("unexpected Jira comment write")
			}
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &f.postInput)
			return jiraResponse(f.postCode, f.post), nil
		case r.URL.Path == "/rest/api/3/attachment/content/"+jiraAttachmentID:
			res := jiraResponse(http.StatusSeeOther, "")
			res.Header.Set("Location", "https://api.media.atlassian.com/collect/"+jiraAttachmentID+"?token=signed")
			return res, nil
		}
		t.Fatalf("unexpected Jira request: %s %s", r.Method, r.URL.Redacted())
		return nil, nil
	})
	return f
}

func TestJiraReadsConversationAndProvenance(t *testing.T) {
	f := newJiraFixture(t)
	s, err := f.source.read(t.Context(), 10042)
	if err != nil {
		t.Fatal(err)
	}
	issueURL := jiraTestOrigin + "/browse/" + jiraIssueKeyValue
	if s.Ticket.ID != 10042 || s.Ticket.Kind != "jira" || s.Ticket.URL != issueURL ||
		s.Ticket.Status != "indeterminate" || s.Ticket.Requester != jiraAccountHash(jiraReporterID) ||
		s.Ticket.RequesterProfile.Name != "Reporter" || s.Ticket.RequesterProfile.Email != "reporter@example.com" ||
		len(s.Ticket.Tags) != 1 || s.Ticket.Tags[0] != "reproduce" {
		t.Fatalf("incorrect issue snapshot: %+v", s.Ticket)
	}
	if len(s.Comments) != 4 {
		t.Fatalf("wrong comment count: %+v", s.Comments)
	}
	// The description is the first pseudo-comment and carries the attachments.
	if s.Comments[0].Body != "Python version: 3.12.4\nExpected: five seconds\nActual: five milliseconds" ||
		!s.Comments[0].Public || s.Comments[0].URL != issueURL {
		t.Fatalf("incorrect description comment: %+v", s.Comments[0])
	}
	files := s.Comments[0].Files
	if len(files) != 1 || files[0].Name != "app.py" || files[0].Size != 17 || files[0].ID != 10001 ||
		files[0].URL != jiraTestOrigin+"/rest/api/3/attachment/content/"+jiraAttachmentID {
		t.Fatalf("missing attachment: %+v", files)
	}
	if s.Comments[1].ID != 10010 || !s.Comments[1].Public ||
		s.Comments[1].URL != issueURL+"?focusedCommentId=10010" || s.Comments[1].Body != "The smallest reproduction." {
		t.Fatalf("incorrect public comment: %+v", s.Comments[1])
	}
	// jsdPublic:false marks a JSM internal note; restricted comments are never
	// requester facing either.
	if s.Comments[2].Public || s.Comments[3].Public {
		t.Fatalf("internal comments counted as public: %+v", s.Comments[2:])
	}
	if s.Ticket.Updated == "" {
		t.Fatal("missing freshness receipt")
	}
}

func TestJiraIdentifiersMapStably(t *testing.T) {
	if jiraAccountHash(jiraReporterID) <= 0 || jiraAccountHash(jiraReporterID) != jiraAccountHash(jiraReporterID) ||
		jiraAccountHash(jiraReporterID) == jiraAccountHash(jiraAgentID) || jiraAccountHash("") != 0 ||
		jiraAccountHash("bad account id!") != 0 {
		t.Fatal("Jira account mapping is not stable and distinct")
	}
	if (stringOrNumber)("10042").value() != 10042 || (stringOrNumber)("042").value() != 0 ||
		(stringOrNumber)("-1").value() != 0 || (stringOrNumber)("x").value() != 0 {
		t.Fatal("Jira id decoding is not strict")
	}
	var numeric stringOrNumber
	if json.Unmarshal([]byte(`10042`), &numeric) != nil || numeric.value() != 10042 {
		t.Fatal("numeric Jira id failed to decode")
	}
}

func TestJiraResolvesKeyURLAndNumericReferences(t *testing.T) {
	f := newJiraFixture(t)
	for _, tc := range []struct {
		reference string
		issueCall bool
	}{
		{jiraIssueKeyValue, true},
		{" " + jiraIssueKeyValue + " ", true},
		{jiraTestOrigin + "/browse/" + jiraIssueKeyValue, true},
		{jiraIssueID, false},
		{" " + jiraIssueID + " ", false},
	} {
		id, err := f.source.resolveReference(t.Context(), tc.reference)
		if err != nil || id != 10042 {
			t.Fatalf("reference %q resolved to %d: %v", tc.reference, id, err)
		}
	}
	for _, reference := range []string{"", "0", "-1", "010042", "4 2", "ops-42", "OPS-", "OPS-0", "OPS-042",
		"OPS-42x", "https://evil.example/browse/" + jiraIssueKeyValue, jiraTestOrigin + "/browse/", "forty-two"} {
		if _, err := f.source.resolveReference(t.Context(), reference); err == nil || !errors.Is(err, ErrJiraReference) {
			t.Fatalf("reference %q produced %v, want ErrJiraReference", reference, err)
		}
	}
	if f.calls != 3 {
		t.Fatalf("numeric or rejected references reached Jira: calls=%d", f.calls)
	}
}

func TestJiraResolveReferenceRejectsUnusableIssues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *jiraFixture)
	}{
		{"another key returned", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, jiraIssueKeyValue, "OPS-99", 1)
		}},
		{"missing id", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"id":"`+jiraIssueID+`"`, `"id":"0"`, 1)
		}},
		{"missing reporter", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"reporter":{"accountId":"`+jiraReporterID+`","displayName":"Reporter","emailAddress":"reporter@example.com"}`, `"reporter":null`, 1)
		}},
		{"unknown issue", func(f *jiraFixture) {
			f.source.client.Transport = jiraTransport(func(*http.Request) (*http.Response, error) {
				return jiraResponse(404, `{"errorMessages":["missing"]}`), nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			tc.change(f)
			if _, err := f.source.resolveReference(t.Context(), jiraIssueKeyValue); err == nil {
				t.Fatal("unusable issue was resolved")
			}
		})
	}
}

func TestJiraRejectsInvalidIssueResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *jiraFixture)
	}{
		{"invalid issue key", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, jiraIssueKeyValue, "42", 1)
		}},
		{"non-numeric id", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"id":"`+jiraIssueID+`"`, `"id":"x"`, 1)
		}},
		{"invalid timestamp", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"updated":"2026-11-11T12:00:00.000+0000"`, `"updated":"2026-11-11T12:00:00Z"`, 1)
		}},
		{"unknown status category", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"key":"indeterminate"`, `"key":"wat"`, 1)
		}},
		{"invalid reporter id", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, jiraReporterID, "bad account!", 1)
		}},
		{"invalid comment page", func(f *jiraFixture) { f.comments = `{}` }},
		{"comment total above page", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"total":3`, `"total":4`, 1)
		}},
		{"comment page over limit", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"total":3`, `"total":101`, 1)
		}},
		{"duplicate comment", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"id":"10012"`, `"id":"10010"`, 1)
		}},
		{"comment without author", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"author":{"accountId":"`+jiraReporterID+`","displayName":"Reporter"},"body":`+jiraADF("Internal aside."), `"author":null,"body":`+jiraADF("Internal aside."), 1)
		}},
		{"invalid comment time", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"created":"2026-11-11T12:02:00.000+0000"`, `"created":"soon"`, 1)
		}},
		{"unauthorized", func(f *jiraFixture) {
			f.source.client.Transport = jiraTransport(func(*http.Request) (*http.Response, error) {
				return jiraResponse(401, `{"errorMessages":["denied"]}`), nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			tc.change(f)
			if _, err := f.source.read(t.Context(), 10042); err == nil || errors.Is(err, ErrChanged) {
				t.Fatalf("invalid Jira response admitted: %v", err)
			}
		})
	}
}

func TestJiraConfirmDetectsIssueAndConversationChanges(t *testing.T) {
	f := newJiraFixture(t)
	s, err := f.source.snapshot(t.Context(), 10042)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.source.confirm(t.Context(), s.Ticket); err != nil {
		t.Fatalf("unchanged issue did not confirm: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*jiraFixture)
	}{
		{"label change", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"labels":["reproduce"]`, `"labels":["other"]`, 1)
		}},
		{"status category change", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `"key":"indeterminate"`, `"key":"done"`, 1)
		}},
		{"attachment removed", func(f *jiraFixture) {
			f.issue = strings.Replace(f.issue, `,"attachment":[{"id":"`+jiraAttachmentID+`","filename":"app.py","size":17,"mimeType":"text/x-python"}]`, "", 1)
		}},
		{"comment edit", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, "The smallest reproduction.", "The smallest known reproduction.", 1)
		}},
		{"comment visibility change", func(f *jiraFixture) {
			f.comments = strings.Replace(f.comments, `"jsdPublic":true`, `"jsdPublic":false`, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.change(f)
			if err := f.source.confirm(t.Context(), s.Ticket); !errors.Is(err, ErrChanged) {
				t.Fatalf("changed issue confirmed: %v", err)
			}
		})
	}
}

func TestJiraAttachmentSelectionSkipsUnsupportedFiles(t *testing.T) {
	j := newJira(jiraTestOrigin, jiraTestEmail, jiraTestToken)
	files := j.attachments([]jiraAttachmentMeta{
		{ID: jiraAttachmentID, Filename: "app.py", Size: 10},
		{ID: "10002", Filename: "notes.txt", Size: 0},
		{ID: "10003", Filename: "script.sh", Size: 10},
		{ID: "10004", Filename: "huge.log", Size: casefile.MaxFileBytes + 1},
		{ID: jiraAttachmentID, Filename: "app.py", Size: 10},
		{ID: "0", Filename: "odd.py", Size: 10},
	})
	if len(files) != 1 || files[0].Name != "app.py" ||
		files[0].URL != jiraTestOrigin+"/rest/api/3/attachment/content/"+jiraAttachmentID {
		t.Fatalf("unexpected attachment selection: %+v", files)
	}
}

func TestJiraFileFetchFollowsOneSignedRedirectWithoutCredentials(t *testing.T) {
	const cleanURL = jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID
	f := newJiraFixture(t)
	if data, err := f.source.file(t.Context(), attachment{Name: "app.py", Size: 17, ID: 10001, URL: cleanURL}); err != nil ||
		string(data) != "print('timeout')\n" || f.mediaRequests != 1 {
		t.Fatalf("clean attachment: %q %v media=%d", data, err, f.mediaRequests)
	}
	// 302 redirects are accepted like the observed 303.
	f = newJiraFixture(t)
	f.source.client.Transport = jiraTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/rest/api/3/attachment/content/"+jiraAttachmentID {
			res := jiraResponse(http.StatusFound, "")
			res.Header.Set("Location", "https://api.media.atlassian.com/collect/"+jiraAttachmentID+"?token=signed")
			return res, nil
		}
		res := jiraResponse(200, "print('timeout')\n")
		return res, nil
	})
	if _, err := f.source.file(t.Context(), attachment{Name: "app.py", Size: 17, ID: 10001, URL: cleanURL}); err != nil {
		t.Fatalf("302 redirect rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		url    string
		size   int64
		body   string
		status int
	}{
		{name: "foreign origin", url: "https://evil.example/rest/api/3/attachment/content/" + jiraAttachmentID, size: 19},
		{name: "query string", url: jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID + "?x=1", size: 19},
		{name: "attachment listing", url: jiraTestOrigin + "/rest/api/3/attachment/content/", size: 19},
		{name: "size changed", url: jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID, size: 3, body: "print('timeout')\n"},
		{name: "denied", url: jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID, size: 19, status: 403},
		{name: "NUL bytes", url: jiraTestOrigin + "/rest/api/3/attachment/content/" + jiraAttachmentID, size: 2, body: "x\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			if tc.body != "" {
				f.download = tc.body
			}
			if tc.status != 0 {
				f.mediaCode = tc.status
			}
			if _, err := f.source.file(t.Context(), attachment{Name: "app.py", Size: tc.size, ID: 10001, URL: tc.url}); err == nil {
				t.Fatal("unsupported Jira attachment was fetched")
			}
		})
	}
	// The signed redirect is followed only to the documented media host.
	for _, location := range []string{
		"https://evil.example/collect/" + jiraAttachmentID + "?token=signed",
		"https://api.media.atlassian.com.evil.example/collect/" + jiraAttachmentID,
		"https://api.media.atlassian.com",
	} {
		f := newJiraFixture(t)
		f.source.client.Transport = jiraTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/rest/api/3/attachment/content/"+jiraAttachmentID {
				res := jiraResponse(http.StatusSeeOther, "")
				res.Header.Set("Location", location)
				return res, nil
			}
			return jiraResponse(200, "x"), nil
		})
		if _, err := f.source.file(t.Context(), attachment{Name: "app.py", Size: 17, ID: 10001, URL: cleanURL}); err == nil {
			t.Fatalf("redirect to %q was followed", location)
		}
	}
}

func TestJiraADFBodiesRoundTrip(t *testing.T) {
	for _, message := range []string{
		"Single line",
		"Two\nlines",
		"Trailing break\n",
		"Blank\n\nline",
		"Windows\r\nline breaks",
		"Control \x00 and \x01 characters dropped",
		"",
	} {
		encoded := jiraADFBody(message)
		if !json.Valid(encoded) {
			t.Fatalf("invalid ADF for %q: %s", message, encoded)
		}
		rendered := jiraADFText(encoded)
		want := strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
		if strings.ContainsRune(want, 0) {
			want = strings.ReplaceAll(want, "\x00", "")
			want = strings.ReplaceAll(want, "\x01", "")
		}
		if rendered != strings.TrimRight(want, "\n") && !(rendered == " " && strings.TrimRight(want, "\n") == "") {
			t.Fatalf("round trip %q rendered as %q", message, rendered)
		}
	}
}

func TestJiraADFTextCollapsesLayout(t *testing.T) {
	doc := `{"content":[
	 {"type":"heading","content":[{"type":"text","text":"Summary"}]},
	 {"type":"paragraph","content":[{"type":"text","text":"line one"},{"type":"hardBreak"},{"type":"text","text":"line two"}]},
	 {"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"bullet"}]}]}]},
	 {"type":"codeBlock","content":[{"type":"text","text":"print(1)"}]},
	 {"type":"table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"cell"}]}]}]}]},
	 {"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"10001"}}]}
	]}`
	text := jiraADFText([]byte(doc))
	for _, want := range []string{"Summary", "line one\nline two", "bullet", "print(1)", "cell"} {
		if !strings.Contains(text, want) {
			t.Fatalf("extracted text %q is missing %q", text, want)
		}
	}
}

func mustJiraTicket(t *testing.T, f *jiraFixture) ticket {
	t.Helper()
	s, err := f.source.snapshot(t.Context(), 10042)
	if err != nil {
		t.Fatal(err)
	}
	return s.Ticket
}

func TestJiraPostCommentValidatesRequestAndReceipt(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name     string
		public   bool
		internal bool
	}{
		{"public clarification", true, false},
		{"internal note", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			f.source.writable = true
			f.post = jiraReceiptJSON("10050", body)
			id, err := f.source.postComment(t.Context(), mustJiraTicket(t, f), body, tc.public)
			if err != nil || id != 10050 {
				t.Fatalf("comment post: id=%d err=%v", id, err)
			}
			if len(f.postInput.Properties) != 1 || f.postInput.Properties[0].Key != "sd.public.comment" ||
				f.postInput.Properties[0].Value.Internal != tc.internal {
				t.Fatalf("comment visibility was not set explicitly: %+v", f.postInput.Properties)
			}
			if jiraADFText(mustMarshal(t, f.postInput.Body)) != body {
				t.Fatalf("comment body did not round-trip: %s", mustMarshal(t, f.postInput.Body))
			}
		})
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestJiraPostCommentRejectsInvalidReceipts(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name     string
		post     string
		postCode int
	}{
		{"edited body", jiraReceiptJSON("10050", "edited"), 201},
		{"wrong issue in self URL", strings.Replace(jiraReceiptJSON("10050", body),
			"/rest/api/3/issue/"+jiraIssueID+"/comment/10050", "/rest/api/3/issue/10099/comment/10050", 1), 201},
		{"invalid id", jiraReceiptJSON("not-a-number", body), 201},
		{"server rejection", `{"errorMessages":["denied"]}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			f.source.writable = true
			f.post, f.postCode = tc.post, tc.postCode
			if _, err := f.source.postComment(t.Context(), mustJiraTicket(t, f), body, true); err == nil {
				t.Fatal("invalid Jira comment receipt accepted")
			}
		})
	}
}

func TestJiraPostCommentIsGated(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name   string
		change func(*jiraFixture, *ticket)
	}{
		{"read-only source", func(f *jiraFixture, _ *ticket) { f.source.writable = false }},
		{"no token", func(f *jiraFixture, _ *ticket) {
			f.source.token = ""
			f.source.authorization = ""
		}},
		{"done issue", func(_ *jiraFixture, t *ticket) { t.Status = "done" }},
		{"foreign URL", func(_ *jiraFixture, t *ticket) {
			t.URL = jiraTestOrigin + "/browse/OPS-99"
		}},
		{"foreign kind", func(_ *jiraFixture, t *ticket) { t.Kind = "github" }},
		{"changed issue", func(f *jiraFixture, _ *ticket) {
			f.comments = strings.Replace(f.comments, "The smallest reproduction.", "racing edit", 1)
		}},
		{"oversized body", func(_ *jiraFixture, _ *ticket) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJiraFixture(t)
			f.source.writable = true
			message := body
			if tc.name == "oversized body" {
				message = strings.Repeat("x", maxJiraCommentBytes+1)
			}
			ticket := mustJiraTicket(t, f)
			tc.change(f, &ticket)
			if _, err := f.source.postComment(t.Context(), ticket, message, true); err == nil {
				t.Fatal("Jira comment write was not gated")
			}
		})
	}
}
