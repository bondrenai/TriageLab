// Package ticketflow manages preparation of helpdesk tickets for investigation.
package ticketflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

const maxProviderBytes = 2 << 20

var subdomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var attachmentHost = regexp.MustCompile(`^p[0-9]+\.zdusercontent\.com$`)

var ErrChanged = errors.New("ticket changed; refresh it before taking action")

type field struct {
	ID    int64 `json:"id"`
	Value any   `json:"value"`
}

type Requester struct {
	ID    int64  `json:"id,string"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

type ticket struct {
	ID        int64    `json:"id"`
	Requester int64    `json:"requester_id"`
	Subject   string   `json:"subject"`
	Status    string   `json:"status"`
	Priority  string   `json:"priority"`
	Tags      []string `json:"tags"`
	Fields    []field  `json:"custom_fields"`
	Updated   string   `json:"updated_at"`

	RequesterProfile Requester `json:"-"`
	Kind             string    `json:"-"`
	URL              string    `json:"-"`
}

type attachment struct {
	ID   int64  `json:"id"`
	Name string `json:"file_name"`
	Size int64  `json:"size"`
	URL  string `json:"content_url"`
}

type comment struct {
	ID     int64        `json:"id"`
	Author int64        `json:"author_id"`
	Public bool         `json:"public"`
	Body   string       `json:"body"`
	Files  []attachment `json:"attachments"`
	URL    string       `json:"url,omitempty"`
}

type snapshot struct {
	Ticket   ticket
	Comments []comment
}

// The flow authorizes and journals each comment before the connector sends it.
type source interface {
	account() string
	ticket(context.Context, int64) (ticket, error)
	read(context.Context, int64) (snapshot, error)
	confirm(context.Context, ticket) error
	file(context.Context, attachment) ([]byte, error)
	postComment(context.Context, ticket, string, bool) (int64, error)
}

type zendesk struct {
	origin string
	client *http.Client
	oauth  *zendeskOAuth
}

func newZendesk(account string) *zendesk {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &zendesk{
		origin: "https://" + account + ".zendesk.com",
		client: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func (z *zendesk) account() string { return z.origin }

func (z *zendesk) request(ctx context.Context, method, target string, body []byte, limit int64) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" || u.Scheme+"://"+u.Host != z.origin {
		return nil, errors.New("Zendesk resource is outside the configured account")
	}
	// Retry one read after a rejected access token. A clarification is never
	// retried here; the case owns the delivery reservation and outcome.
	data, err := z.requestAuthorized(ctx, method, u.String(), body, limit)
	if method == http.MethodGet && errors.Is(err, errRejectedAccess) {
		data, err = z.requestAuthorized(ctx, method, u.String(), body, limit)
	}
	if errors.Is(err, errRejectedAccess) {
		return nil, ErrOAuthRequired
	}
	return data, err
}

var errRejectedAccess = errors.New("Zendesk rejected the OAuth access token")

func (z *zendesk) requestAuthorized(ctx context.Context, method, target string, body []byte, limit int64) ([]byte, error) {
	token, err := z.oauth.access(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid Zendesk request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if strings.HasPrefix(req.URL.Path, "/api/v2/tickets/") {
		req.Header.Set("Accept", "application/json")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := z.client.Do(req)
	if err != nil {
		return nil, errors.New("Zendesk request could not be completed")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		z.oauth.rejected(token)
		return nil, errRejectedAccess
	}
	if res.StatusCode == http.StatusConflict {
		return nil, ErrChanged
	}
	if res.StatusCode == http.StatusFound && method == http.MethodGet && attachmentPath(req.URL.Path) {
		return z.downloadAttachment(ctx, res.Header.Get("Location"), limit)
	}
	return readZendeskResponse(res, limit)
}

func readZendeskResponse(res *http.Response, limit int64) ([]byte, error) {
	if res.StatusCode != http.StatusOK {
		return nil, zendeskStatus(res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Zendesk response is incomplete or too large")
	}
	return data, nil
}

type zendeskStatus int

func (status zendeskStatus) Error() string { return fmt.Sprintf("Zendesk returned HTTP %d", status) }

func (z *zendesk) downloadAttachment(ctx context.Context, target string, limit int64) ([]byte, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !attachmentHost.MatchString(u.Host) {
		return nil, errors.New("unsupported Zendesk attachment redirect")
	}
	// Zendesk authorizes the download on the account host. The temporary URL
	// carries its own authority, so start a fresh request without OAuth headers.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid Zendesk attachment redirect")
	}
	res, err := z.client.Do(req)
	if err != nil {
		return nil, errors.New("Zendesk attachment download could not be completed")
	}
	defer res.Body.Close()
	return readZendeskResponse(res, limit)
}

func (z *zendesk) ticket(ctx context.Context, id int64) (ticket, error) {
	var response struct {
		Ticket ticket `json:"ticket"`
		Users  []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"users"`
	}
	target := z.origin + "/api/v2/tickets/" + strconv.FormatInt(id, 10) + ".json"
	data, err := z.request(ctx, http.MethodGet, target+"?include=users", nil, maxProviderBytes)
	if errors.Is(err, zendeskStatus(http.StatusForbidden)) {
		data, err = z.request(ctx, http.MethodGet, target, nil, maxProviderBytes)
	}
	if err != nil {
		return ticket{}, err
	}
	if json.Unmarshal(data, &response) != nil || response.Ticket.ID != id || response.Ticket.Requester <= 0 {
		return ticket{}, errors.New("Zendesk returned an invalid ticket")
	}
	if _, err := time.Parse(time.RFC3339Nano, response.Ticket.Updated); err != nil {
		return ticket{}, errors.New("Zendesk ticket has no valid update time")
	}
	response.Ticket.RequesterProfile.ID = response.Ticket.Requester
	for _, user := range response.Users {
		if user.ID == response.Ticket.Requester {
			response.Ticket.RequesterProfile.Name = user.Name
			response.Ticket.RequesterProfile.Email = user.Email
			break
		}
	}
	return response.Ticket, nil
}

func (z *zendesk) read(ctx context.Context, id int64) (snapshot, error) {
	t, err := z.ticket(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	var page struct {
		Comments []comment `json:"comments"`
		Meta     struct {
			More *bool `json:"has_more"`
		} `json:"meta"`
	}
	data, err := z.request(ctx, http.MethodGet, z.origin+"/api/v2/tickets/"+strconv.FormatInt(id, 10)+"/comments?page%5Bsize%5D=100&sort=created_at", nil, maxProviderBytes)
	if err != nil {
		return snapshot{}, err
	}
	if json.Unmarshal(data, &page) != nil || page.Comments == nil || page.Meta.More == nil || len(page.Comments) > 100 {
		return snapshot{}, errors.New("Zendesk returned an invalid comment page")
	}
	if *page.Meta.More {
		return snapshot{}, errors.New("ticket exceeds the 100-comment limit; review it in Zendesk")
	}
	if err := z.confirm(ctx, t); err != nil {
		return snapshot{}, err
	}
	return snapshot{Ticket: t, Comments: page.Comments}, nil
}

func (z *zendesk) confirm(ctx context.Context, t ticket) error {
	current, err := z.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

func (z *zendesk) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || !attachmentPath(u.Path) {
		return nil, errors.New("unsupported Zendesk attachment location")
	}
	data, err := z.request(ctx, http.MethodGet, f.URL, nil, casefile.MaxFileBytes)
	if err == nil && int64(len(data)) != f.Size {
		return nil, errors.New("Zendesk attachment size changed")
	}
	return data, err
}

func attachmentPath(path string) bool {
	return (strings.HasPrefix(path, "/attachments/") || strings.HasPrefix(path, "/api/v2/attachments/")) &&
		!strings.Contains(path, "..") && !strings.Contains(path, "\\")
}

func (z *zendesk) postComment(ctx context.Context, t ticket, message string, public bool) (int64, error) {
	if z.oauth.scope != zendeskWriteScopes {
		return 0, ErrOAuthRequired
	}
	body, err := json.Marshal(map[string]any{"ticket": map[string]any{
		"safe_update": true, "updated_stamp": t.Updated,
		"comment": map[string]any{"body": message, "public": public},
	}})
	if err != nil {
		return 0, err
	}
	data, err := z.request(ctx, http.MethodPut, z.origin+"/api/v2/tickets/"+strconv.FormatInt(t.ID, 10)+".json", body, maxProviderBytes)
	if err != nil {
		return 0, err
	}
	var result struct {
		Ticket ticket `json:"ticket"`
		Audit  struct {
			TicketID int64 `json:"ticket_id"`
			Events   []struct {
				ID     int64  `json:"id"`
				Type   string `json:"type"`
				Public *bool  `json:"public"`
			} `json:"events"`
		} `json:"audit"`
	}
	if json.Unmarshal(data, &result) != nil || result.Ticket.ID != t.ID {
		return 0, errors.New("Zendesk did not confirm the ticket update")
	}
	if public {
		return 0, nil
	}
	var id int64
	if result.Audit.TicketID == t.ID {
		for _, event := range result.Audit.Events {
			if event.Type != "Comment" {
				continue
			}
			if id != 0 || event.ID <= 0 || event.Public == nil || *event.Public {
				return 0, errors.New("Zendesk returned an ambiguous internal note receipt")
			}
			id = event.ID
		}
	}
	if id == 0 {
		return 0, errors.New("Zendesk did not return an internal note receipt")
	}
	return id, nil
}
