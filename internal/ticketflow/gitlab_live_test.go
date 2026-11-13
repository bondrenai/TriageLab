package ticketflow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

// Live, opt-in end-to-end checks against a disposable self-managed GitLab.
// They are skipped unless all three environment variables point at an
// instance dedicated to this run; nothing here may touch a real project:
//
//	TRIAGELAB_GITLAB_LIVE_ORIGIN   e.g. http://127.0.0.1:8090
//	TRIAGELAB_GITLAB_LIVE_TOKEN    a personal access token with api scope
//	TRIAGELAB_GITLAB_LIVE_GROUP    a scratch group name the test owns
//
// The test creates and deletes that group, so the instance must be
// disposable. It exercises connection verification, issue and note reads,
// system-note filtering, Markdown upload download, reference resolution,
// evidence preparation through a real approval, clarification delivery
// with a bounded follow-up, and investigation-comment delivery — all
// against the live REST v4 API. It launches no sandbox and calls no model.

const gitLabLiveFile = "print('timeout')\n"

func gitLabLiveTarget(t *testing.T) (origin, token, group string) {
	t.Helper()
	origin, token, group = os.Getenv("TRIAGELAB_GITLAB_LIVE_ORIGIN"),
		os.Getenv("TRIAGELAB_GITLAB_LIVE_TOKEN"), os.Getenv("TRIAGELAB_GITLAB_LIVE_GROUP")
	if origin == "" || token == "" || group == "" {
		t.Skip("set TRIAGELAB_GITLAB_LIVE_ORIGIN, _TOKEN, and _GROUP against a disposable self-managed GitLab to run live checks")
	}
	return origin, token, group
}

// gitLabLiveCall performs one bounded API call with the instance token.
func gitLabLiveCall(t *testing.T, origin, token, method, path string, body []byte, contentType string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(origin, "/")+"/api/v4"+path, reader)
	if err != nil {
		t.Fatalf("invalid live request: %v", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("live GitLab request failed: %v", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, data
}

// gitLabLiveCleanup removes the scratch group best-effort. It must not use
// t.Context (canceled once the test body returns) and must not fail the
// test: a leftover group is retried by the next bootstrap.
func gitLabLiveCleanup(t *testing.T, origin, token, group string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
		strings.TrimSuffix(origin, "/")+"/api/v4/groups/"+group, nil)
	if err != nil {
		return
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	res, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		t.Logf("live GitLab cleanup: %v", err)
		return
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
}

// gitLabLiveBootstrap creates a scratch group and project with one ready
// issue (facts in the description, an uploaded reproduction on a note, a
// label, and a system note) plus one issue that lacks evidence. It returns
// the project path and both issue iids.
func gitLabLiveBootstrap(t *testing.T, origin, token, group string) (project string, ready, ask int64) {
	t.Helper()
	// The group belongs to this run; recreate it if a previous run left it.
	// GitLab deletes groups asynchronously, so the name can stay reserved
	// for a while: creation is retried until the instance releases it.
	gitLabLiveCleanup(t, origin, token, group)
	var groupID int64
	for attempt := 0; ; attempt++ {
		status, data := gitLabLiveCall(t, origin, token, http.MethodPost, "/groups",
			[]byte(`{"path":`+jsonString(group)+`,"name":`+jsonString(group)+`,"visibility":"private"}`), "application/json")
		if status == http.StatusCreated {
			var created struct {
				ID int64 `json:"id"`
			}
			if json.Unmarshal(data, &created) != nil || created.ID <= 0 {
				t.Fatalf("live group response was invalid: %s", data)
			}
			groupID = created.ID
			break
		}
		if attempt >= 12 {
			t.Fatalf("live group creation returned HTTP %d: %s", status, data)
		}
		time.Sleep(5 * time.Second)
	}
	project = group + "/repro"
	status, data := gitLabLiveCall(t, origin, token, http.MethodPost, "/projects",
		[]byte(`{"path":"repro","name":"repro","namespace_id":`+strconv.FormatInt(groupID, 10)+`,"visibility":"private"}`), "application/json")
	if status != http.StatusCreated {
		t.Fatalf("live project creation returned HTTP %d: %s", status, data)
	}
	var projectInfo struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(data, &projectInfo) != nil || projectInfo.ID <= 0 {
		t.Fatalf("live project response was invalid: %s", data)
	}

	// Upload the reproduction the way a reporter would, then reference the
	// returned Markdown from a note on the ready issue.
	var boundary = "triagelab-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	if err := writer.SetBoundary(boundary); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "app.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(gitLabLiveFile)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, data = gitLabLiveCall(t, origin, token, http.MethodPost,
		"/projects/"+strconv.FormatInt(projectInfo.ID, 10)+"/uploads", upload.Bytes(), writer.FormDataContentType())
	if status != http.StatusCreated {
		t.Fatalf("live upload returned HTTP %d: %s", status, data)
	}
	var uploaded struct {
		Markdown string `json:"markdown"`
	}
	if json.Unmarshal(data, &uploaded) != nil || !strings.Contains(uploaded.Markdown, "/uploads/") {
		t.Fatalf("live upload response was invalid: %s", data)
	}

	status, data = gitLabLiveCall(t, origin, token, http.MethodPost,
		"/projects/"+strconv.FormatInt(projectInfo.ID, 10)+"/issues",
		[]byte(`{"title":"Timeout is too short","labels":"reproduce","description":`+
			jsonString("Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds.")+`}`), "application/json")
	if status != http.StatusCreated {
		t.Fatalf("live ready issue creation returned HTTP %d: %s", status, data)
	}
	var issue struct {
		IID int64 `json:"iid"`
	}
	if json.Unmarshal(data, &issue) != nil || issue.IID <= 0 {
		t.Fatalf("live ready issue response was invalid: %s", data)
	}
	ready = issue.IID

	noteBody := "The smallest reproduction.\n" + uploaded.Markdown
	status, _ = gitLabLiveCall(t, origin, token, http.MethodPost,
		"/projects/"+strconv.FormatInt(projectInfo.ID, 10)+"/issues/"+strconv.FormatInt(ready, 10)+"/notes",
		[]byte(`{"body":`+jsonString(noteBody)+`}`), "application/json")
	if status != http.StatusCreated {
		t.Fatalf("live note creation returned HTTP %d", status)
	}
	// A later label change leaves a system note that must never count as
	// conversation.
	status, _ = gitLabLiveCall(t, origin, token, http.MethodPut,
		"/projects/"+strconv.FormatInt(projectInfo.ID, 10)+"/issues/"+strconv.FormatInt(ready, 10),
		[]byte(`{"add_labels":"extra"}`), "application/json")
	if status != http.StatusOK {
		t.Fatalf("live label update returned HTTP %d", status)
	}

	status, data = gitLabLiveCall(t, origin, token, http.MethodPost,
		"/projects/"+strconv.FormatInt(projectInfo.ID, 10)+"/issues",
		[]byte(`{"title":"Missing details","labels":"reproduce","description":"My Python script times out."}`), "application/json")
	if status != http.StatusCreated {
		t.Fatalf("live ask issue creation returned HTTP %d: %s", status, data)
	}
	if json.Unmarshal(data, &issue) != nil || issue.IID <= 0 {
		t.Fatalf("live ask issue response was invalid: %s", data)
	}
	ask = issue.IID
	return project, ready, ask
}

func gitLabLiveFlowConfig(origin, project, token string) Config {
	return Config{Source: "gitlab", Repository: project, Origin: origin, InvestigationComments: true,
		Policy: Policy{Tag: "reproduce", AutoQuestions: true, MaxQuestions: 2}}.WithToken(token)
}

// TestLiveGitLabConnectorAndFlow runs the full connector and preparation
// workflow against the disposable instance.
func TestLiveGitLabConnectorAndFlow(t *testing.T) {
	origin, token, group := gitLabLiveTarget(t)
	project, ready, _ := gitLabLiveBootstrap(t, origin, token, group)
	t.Cleanup(func() { gitLabLiveCleanup(t, origin, token, group) })
	config := gitLabLiveFlowConfig(origin, project, token)
	if err := VerifySource(t.Context(), config); err != nil {
		t.Fatalf("connection verification failed: %v", err)
	}

	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()

	// A pasted issue URL resolves to the numeric case key without a request.
	issueURL := strings.TrimSuffix(origin, "/") + "/" + project + "/-/issues/" + strconv.FormatInt(ready, 10)
	if id, err := flow.ResolveTicket(t.Context(), issueURL+"#note_1"); err != nil || id != ready {
		t.Fatalf("live issue URL resolved to %d: %v", id, err)
	}

	preview, err := flow.Preview(t.Context(), ready)
	if err != nil || preview.State != "approval" {
		t.Fatalf("live ready issue did not reach approval: %+v, %v", preview, err)
	}
	if len(preview.Facts) != 3 || len(preview.Files) != 1 || preview.Files[0].Path != "app.py" ||
		preview.Files[0].SHA256 == "" || preview.Files[0].SourceURL == "" {
		t.Fatalf("live evidence selection was wrong: %+v %+v", preview.Facts, preview.Files)
	}
	tracked, err := flow.Track(t.Context(), ready, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	launched, err := flow.Approve(t.Context(), ready, tracked.Plan, func(input casefile.Case, _ RunTarget, reserve func() error) error {
		source = input.Source
		if !strings.Contains(strings.Join(input.Evidence, "\n"), "attached at ") {
			t.Fatalf("live attachment provenance missing: %v", input.Evidence)
		}
		return reserve()
	})
	if err != nil || launched.RunID == "" {
		t.Fatalf("live approval did not reserve a run: %+v, %v", launched, err)
	}
	if source["kind"] != "gitlab" || source["gitlab_project"] != project || source["issue_iid"] != strconv.FormatInt(ready, 10) {
		t.Fatalf("live report source was wrong: %+v", source)
	}

	// The system note from the label change must not appear as conversation.
	s, err := flow.source.read(t.Context(), ready)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Comments {
		if strings.Contains(c.Body, "added 1 label") {
			t.Fatalf("system note entered the conversation: %+v", c)
		}
	}

	saved := map[string]any{
		"run_id": launched.RunID, "status": "completed", "outcome": "reproduced",
		"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
		"case":        map[string]any{"source": source},
		"analysis":    map[string]any{"summary": "The deadline was reproduced. Five seconds passed."},
	}
	notePreview := flow.NotePreview(launched.RunID, saved)
	if notePreview == nil || !notePreview.CanPost || notePreview.Plan == "" {
		t.Fatalf("live investigation comment preview was not ready: %+v", notePreview)
	}
	note, err := flow.PostNote(t.Context(), launched.RunID, saved, notePreview.Plan)
	if err != nil || note.Status != "posted" || note.CommentID <= 0 {
		t.Fatalf("live investigation comment was not delivered: %+v, %v", note, err)
	}
	s, err = flow.source.read(t.Context(), ready)
	if err != nil {
		t.Fatal(err)
	}
	if last := s.Comments[len(s.Comments)-1]; last.ID != note.CommentID || !strings.Contains(last.Body, launched.RunID) {
		t.Fatalf("live investigation comment receipt did not match the issue conversation: %+v", last)
	}
}

// TestLiveGitLabClarificationFollowup exercises clarification delivery and
// the requester-reply rule on the disposable instance.
func TestLiveGitLabClarificationFollowup(t *testing.T) {
	origin, token, group := gitLabLiveTarget(t)
	project, _, ask := gitLabLiveBootstrap(t, origin, token, group)
	t.Cleanup(func() { gitLabLiveCleanup(t, origin, token, group) })
	config := gitLabLiveFlowConfig(origin, project, token)
	flow, err := Open(config, filepath.Join(t.TempDir(), "cases"), investigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()

	c, err := flow.Preview(t.Context(), ask)
	if err != nil || c.State != "ask" || c.Draft == "" {
		t.Fatalf("live ask issue did not draft a clarification: %+v, %v", c, err)
	}
	if c, err = flow.Track(t.Context(), ask, c.Revision); err != nil {
		t.Fatal(err)
	}
	c, err = flow.Clarify(t.Context(), ask, c.Revision)
	if err != nil || len(c.Questions) != 1 || c.Questions[0].Status != "sent" || c.Questions[0].CommentID <= 0 {
		t.Fatalf("live clarification was not delivered: %+v, %v", c, err)
	}
	if c, err = flow.Refresh(t.Context(), ask); err != nil || c.State != "waiting" {
		t.Fatalf("live clarification did not wait: %+v, %v", c, err)
	}
	// A reply posted directly in GitLab by the same account still counts as
	// the requester answering (only our own receipts are pinned out), and
	// the reviewed policy permits one bounded follow-up.
	status, data := gitLabLiveCall(t, origin, token, http.MethodPost,
		"/projects/"+gitlabProjectPath(project)+"/issues/"+strconv.FormatInt(ask, 10)+"/notes",
		[]byte(`{"body":"Python version: 3.12.4"}`), "application/json")
	if status != http.StatusCreated {
		t.Fatalf("live requester reply returned HTTP %d: %s", status, data)
	}
	if c, err = flow.Refresh(t.Context(), ask); err != nil || c.State != "waiting" || len(c.Questions) != 2 {
		t.Fatalf("live requester reply did not permit the bounded follow-up: %+v, %v", c, err)
	}
}
