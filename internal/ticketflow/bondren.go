package ticketflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

// The issue, message, and attachment shapes are pinned to the Bondren API
// surface (bondren2 openapi.yaml) reviewed on 2026-11-09: issues are UUID
// resources with a tenant-wide number, conversations are messages with
// public/internal visibility, attachments download through an authorized
// clean-bytes endpoint, and agent tokens authorize as HTTP bearers. Re-review
// the Bondren API before changing these shapes.
const maxBondrenCommentBytes = 16 << 10
const maxBondrenMessages = 100

// ErrReference rejects a workspace reference that is neither a Bondren issue
// ID nor a positive issue number, before any request reaches Bondren.
var ErrReference = errors.New("use the Bondren issue ID from the issue URL or a positive issue number")

// Bondren resolves issues for the flow's numeric case keys through the issue
// by-ID endpoint when the ID is known, and falls back to the saved-view query
// endpoint's number equality filter otherwise. Both endpoints must stay
// available in Bondren.
var bondrenUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var bondrenAttachmentPath = regexp.MustCompile(`^/api/v1/issues/([0-9a-f-]{36})/attachments/([0-9a-f-]{36})/download$`)

type bondrenSource struct {
	origin   string
	token    string
	writable bool
	client   *http.Client

	// issues maps the numeric case key (the issue number) to the issue UUID
	// so refreshes can use the by-ID endpoint. Seeded from stored case URLs
	// and from every successful ID or number lookup.
	mu     sync.Mutex
	issues map[int64]string
}

func newBondren(origin, token string) *bondrenSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &bondrenSource{origin: strings.TrimSuffix(origin, "/"), token: token,
		issues: make(map[int64]string), client: &http.Client{
			Transport: transport, Timeout: 8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}}
}

func (b *bondrenSource) account() string { return b.origin }

func (b *bondrenSource) issueURL(uuid string) string {
	return b.origin + "/work/support/" + uuid
}

// Bondren message URLs are the issue conversation anchored at the message.
func (b *bondrenSource) messageURL(issueUUID, messageUUID string) string {
	return b.issueURL(issueUUID) + "#message-" + messageUUID
}

// bondrenIssueUUID recovers the issue UUID pinned by a stored case URL, or ""
// when the URL does not belong to the origin's support workspace.
func bondrenIssueUUID(caseURL, account string) string {
	uuid := strings.TrimPrefix(caseURL, account+"/work/support/")
	if bondrenUUID.MatchString(uuid) {
		return uuid
	}
	return ""
}

// Bondren entity IDs are UUIDs while the shared case store keys requesters and
// comments by 64-bit values. bondrenID maps a UUID to a stable positive value.
func bondrenID(uuid string) int64 {
	if !bondrenUUID.MatchString(uuid) {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(uuid))
	return int64(h.Sum64() & (1<<63 - 1))
}

type bondrenIssue struct {
	ID               string   `json:"id"`
	Number           int64    `json:"number"`
	Title            string   `json:"title"`
	State            string   `json:"state"`
	Tags             []string `json:"tags"`
	OperationalState string   `json:"operational_state"`
	RequesterID      string   `json:"requester_id"`
	RequesterName    string   `json:"requester_name"`
	RequesterEmail   string   `json:"requester_email"`
	Updated          string   `json:"updated_at"`
}

type bondrenAttachment struct {
	ID              string `json:"id"`
	Filename        string `json:"filename"`
	ByteSize        int64  `json:"byte_size"`
	InspectionState string `json:"inspection_state"`
}

type bondrenMessage struct {
	ID            string              `json:"id"`
	IssueID       string              `json:"issue_id"`
	Body          string              `json:"body"`
	CreatedAt     string              `json:"created_at"`
	Visibility    string              `json:"visibility"`
	AuthorKind    string              `json:"author_kind"`
	AuthorUserID  string              `json:"author_user_id"`
	AuthorEndUser string              `json:"author_end_user_id"`
	SourceRemoved bool                `json:"source_removed"`
	Attachments   []bondrenAttachment `json:"attachments"`
}

// call performs one bounded request against the configured Bondren origin.
// Reads may retry nothing; writes are never retried here.
func (b *bondrenSource) call(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, error) {
	target, err := url.Parse(b.origin + path)
	if err != nil || target.Scheme+"://"+target.Host != b.origin || target.User != nil || target.Fragment != "" || target.RawPath != "" {
		return nil, errors.New("Bondren resource is outside the configured origin")
	}
	var reader io.Reader
	if body != nil {
		// An opaque body leaves GetBody unset so the HTTP transport cannot
		// replay this request, even on a reused connection.
		reader = io.NopCloser(bytes.NewReader(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, errors.New("invalid Bondren request")
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TriageLab")
	req.Header.Set("Authorization", "Bearer "+b.token)
	res, err := b.client.Do(req)
	if err != nil {
		return nil, errors.New("Bondren request could not be completed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		// Never echo provider response bodies.
		return nil, fmt.Errorf("Bondren returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Bondren response is incomplete or too large")
	}
	return data, nil
}

// resolve maps the tenant-wide issue number to the issue resource. The number
// filter is an equality saved-view clause; more than one match, or none,
// rejects the lookup.
type bondrenFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type bondrenFilterGroup struct {
	Op      string          `json:"op"`
	Filters []bondrenFilter `json:"filters"`
}

type bondrenIssueQuery struct {
	Limit       int                `json:"limit"`
	FilterGroup bondrenFilterGroup `json:"filter_group"`
}

func (b *bondrenSource) resolve(ctx context.Context, number int64) (bondrenIssue, error) {
	query, err := json.Marshal(bondrenIssueQuery{Limit: 2, FilterGroup: bondrenFilterGroup{Op: "and",
		Filters: []bondrenFilter{{Field: "number", Op: "eq", Value: strconv.FormatInt(number, 10)}}}})
	if err != nil {
		return bondrenIssue{}, errors.New("invalid Bondren request")
	}
	data, err := b.call(ctx, http.MethodPost, "/api/v1/issues/query", query, maxProviderBytes)
	if err != nil {
		return bondrenIssue{}, err
	}
	var page struct {
		Items []bondrenIssue `json:"items"`
	}
	if json.Unmarshal(data, &page) != nil || len(page.Items) != 1 {
		return bondrenIssue{}, errors.New("Bondren returned no unique issue for this number; use the issue ID from the Bondren issue URL, or verify the number")
	}
	return page.Items[0], nil
}

// issueByUUID reads one issue through the by-ID endpoint and checks it
// against the numeric case key the ID was resolved for.
func (b *bondrenSource) issueByUUID(ctx context.Context, uuid string, number int64) (bondrenIssue, error) {
	data, err := b.call(ctx, http.MethodGet, "/api/v1/issues/"+uuid, nil, maxProviderBytes)
	if err != nil {
		return bondrenIssue{}, err
	}
	var issue bondrenIssue
	if json.Unmarshal(data, &issue) != nil || issue.ID != uuid || issue.Number != number {
		return bondrenIssue{}, errors.New("Bondren returned an invalid issue")
	}
	return issue, nil
}

// resolveReference accepts a workspace reference — the issue ID (UUID) from
// the Bondren issue URL or a positive issue number — and returns the numeric
// case key, which is always the issue number. ID lookups register the
// number-to-ID mapping so later snapshots can use the by-ID endpoint.
func (b *bondrenSource) resolveReference(ctx context.Context, reference string) (int64, error) {
	if uuid := strings.ToLower(strings.TrimSpace(reference)); bondrenUUID.MatchString(uuid) {
		data, err := b.call(ctx, http.MethodGet, "/api/v1/issues/"+uuid, nil, maxProviderBytes)
		if err != nil {
			return 0, err
		}
		var issue bondrenIssue
		if json.Unmarshal(data, &issue) != nil || issue.ID != uuid || issue.Number <= 0 {
			return 0, errors.New("Bondren returned an invalid issue for this ID")
		}
		if err := checkBondrenIssue(issue); err != nil {
			return 0, err
		}
		b.remember(issue.Number, issue.ID)
		return issue.Number, nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(reference), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != strings.TrimSpace(reference) {
		return 0, ErrReference
	}
	return id, nil
}

// remember records the issue UUID behind a numeric case key, and seed adds
// the mappings pinned by stored case URLs so refreshes survive restarts.
func (b *bondrenSource) remember(number int64, uuid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.issues[number] = uuid
}

func (b *bondrenSource) issueUUIDFor(number int64) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	uuid, ok := b.issues[number]
	return uuid, ok
}

func (b *bondrenSource) seed(cases []Case) {
	for _, c := range cases {
		if uuid := bondrenIssueUUID(c.URL, b.origin); uuid != "" {
			b.remember(c.TicketID, uuid)
		}
	}
}

func validBondrenTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// bondrenIssueState fails closed on states outside the documented mailbox
// state machine.
func bondrenIssueState(state string) bool {
	switch state {
	case "new", "on_you", "on_customer", "on_hold", "closed":
		return true
	}
	return false
}

func checkBondrenIssue(issue bondrenIssue) error {
	if !bondrenUUID.MatchString(issue.ID) || issue.Number <= 0 || !validBondrenTime(issue.Updated) ||
		!bondrenIssueState(issue.State) || bondrenID(issue.RequesterID) == 0 {
		return errors.New("Bondren returned an invalid issue")
	}
	// Mirrored issues keep their conversation of record in the connected
	// source; Bondren must stay the system of record for an investigation.
	if issue.OperationalState != "native" {
		return errors.New("the issue is managed by a connected source; review it there instead")
	}
	return nil
}

func (b *bondrenSource) snapshot(ctx context.Context, id int64) (snapshot, error) {
	if id <= 0 {
		return snapshot{}, errors.New("use a positive Bondren issue number")
	}
	// A known issue ID uses the by-ID endpoint; otherwise the number has to be
	// resolved through the saved-view query filter.
	var issue bondrenIssue
	var err error
	uuid, known := b.issueUUIDFor(id)
	if known {
		issue, err = b.issueByUUID(ctx, uuid, id)
	} else {
		issue, err = b.resolve(ctx, id)
	}
	if err != nil {
		return snapshot{}, err
	}
	if issue.Number != id {
		return snapshot{}, errors.New("Bondren returned an invalid issue")
	}
	if err := checkBondrenIssue(issue); err != nil {
		return snapshot{}, err
	}
	if !known {
		b.remember(id, issue.ID)
	}
	data, err := b.call(ctx, http.MethodGet, "/api/v1/issues/"+issue.ID+"/messages?limit=100", nil, maxProviderBytes)
	if err != nil {
		return snapshot{}, err
	}
	var page struct {
		Items    []bondrenMessage `json:"items"`
		PageInfo struct {
			HasMore bool `json:"has_more"`
		} `json:"page_info"`
	}
	if json.Unmarshal(data, &page) != nil || page.Items == nil || len(page.Items) > maxBondrenMessages {
		return snapshot{}, errors.New("Bondren returned an invalid message page")
	}
	// Do not follow provider-supplied pagination. More than 100 messages, or a
	// page that reports more, requires review at the source.
	if page.PageInfo.HasMore {
		return snapshot{}, errors.New("issue exceeds the 100-message limit; review it in Bondren")
	}
	requester := bondrenID(issue.RequesterID)
	t := ticket{ID: id, Requester: requester, Subject: issue.Title, Status: issue.State,
		Kind: "bondren", URL: b.issueURL(issue.ID), uuid: issue.ID,
		RequesterProfile: Requester{ID: requester, Name: bounded(issue.RequesterName, 200), Email: bounded(issue.RequesterEmail, 320)}}
	for _, tag := range issue.Tags {
		t.Tags = append(t.Tags, bounded(tag, 100))
	}
	result := snapshot{Ticket: t}
	seen := make(map[int64]bool, len(page.Items))
	for _, m := range page.Items {
		if m.SourceRemoved {
			continue
		}
		id := bondrenID(m.ID)
		var author int64
		switch m.AuthorKind {
		case "end_user":
			author = bondrenID(m.AuthorEndUser)
		case "user":
			author = bondrenID(m.AuthorUserID)
		}
		if id == 0 || seen[id] || author == 0 || m.IssueID != issue.ID ||
			(m.Visibility != "public" && m.Visibility != "internal") || !validBondrenTime(m.CreatedAt) {
			return snapshot{}, errors.New("Bondren returned an invalid issue message")
		}
		seen[id] = true
		result.Comments = append(result.Comments, comment{ID: id, Author: author,
			Public: m.Visibility == "public", Body: m.Body,
			URL: b.messageURL(issue.ID, m.ID), Files: b.attachments(issue.ID, m.Attachments)})
	}
	// The issue updated_at alone does not prove the conversation is unchanged.
	// Include the complete bounded snapshot in the freshness receipt so confirm
	// detects message additions and deletions as well.
	result.Ticket.Updated = digest(struct {
		Issue    bondrenIssue
		Messages []bondrenMessage
	}{issue, page.Items})
	return result, nil
}

// Only attachments that passed inspection are downloadable as clean bytes;
// pending or rejected files stay at the source.
func (b *bondrenSource) attachments(issueUUID string, files []bondrenAttachment) []attachment {
	var result []attachment
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if f.InspectionState != "clean" || bondrenID(f.ID) == 0 || f.ByteSize <= 0 ||
			f.ByteSize > casefile.MaxFileBytes || seen[f.ID] || !fileName.MatchString(reproductionName(f.Filename)) {
			continue
		}
		seen[f.ID] = true
		result = append(result, attachment{Name: f.Filename, Size: f.ByteSize,
			URL: b.origin + "/api/v1/issues/" + issueUUID + "/attachments/" + f.ID + "/download"})
	}
	return result
}

func (b *bondrenSource) ticket(ctx context.Context, id int64) (ticket, error) {
	s, err := b.snapshot(ctx, id)
	return s.Ticket, err
}

func (b *bondrenSource) read(ctx context.Context, id int64) (snapshot, error) {
	s, err := b.snapshot(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if err := b.confirm(ctx, s.Ticket); err != nil {
		return snapshot{}, err
	}
	return s, nil
}

func (b *bondrenSource) confirm(ctx context.Context, t ticket) error {
	current, err := b.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

func (b *bondrenSource) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme+"://"+u.Host != b.origin || u.User != nil || u.Fragment != "" || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || !bondrenAttachmentPath.MatchString(u.Path) ||
		f.Name == "" || !fileName.MatchString(reproductionName(f.Name)) {
		return nil, errors.New("unsupported Bondren attachment location")
	}
	// Unlike public issue trackers, Bondren authorizes attachment downloads
	// with the configured agent token; it only ever travels to the origin.
	data, err := b.call(ctx, http.MethodGet, u.Path, nil, casefile.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != f.Size {
		return nil, errors.New("Bondren attachment size changed")
	}
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("Bondren attachments must be nonempty UTF-8 text files")
	}
	return data, nil
}

func (b *bondrenSource) postComment(ctx context.Context, t ticket, message string, public bool) (int64, error) {
	// Bondren messages carry explicit visibility. Clarifications are public to
	// the requester; investigation notes are internal. Only an explicitly
	// enabled writer may post, and the flow journals its reservation before
	// calling.
	visibility := "internal"
	if public {
		visibility = "public"
	}
	if !b.writable || b.token == "" || t.ID <= 0 || t.Kind != "bondren" ||
		!openForInvestigation("bondren", t.Status) || t.uuid == "" || t.URL != b.issueURL(t.uuid) {
		return 0, ErrBlocked
	}
	if strings.TrimSpace(message) == "" || len(message) > maxBondrenCommentBytes || !utf8.ValidString(message) || strings.ContainsRune(message, '\x00') {
		return 0, errors.New("Bondren messages must be nonempty UTF-8 text of at most 16 KiB")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Bondren has no conditional writes for messages. Recheck immediately
	// before sending, but a concurrent edit can still race the POST.
	if err := b.confirm(ctx, t); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	body, err := json.Marshal(struct {
		Body       string `json:"body"`
		Visibility string `json:"visibility"`
	}{message, visibility})
	if err != nil {
		return 0, errors.New("could not encode the Bondren message")
	}
	data, err := b.call(ctx, http.MethodPost, "/api/v1/issues/"+t.uuid+"/messages", body, maxProviderBytes)
	if err != nil {
		return 0, err
	}
	var receipt bondrenMessage
	if json.Unmarshal(data, &receipt) != nil || bondrenID(receipt.ID) == 0 || receipt.IssueID != t.uuid ||
		receipt.Body != strings.TrimSpace(message) || receipt.Visibility != visibility {
		return 0, errors.New("Bondren did not return a matching message receipt")
	}
	return bondrenID(receipt.ID), nil
}
