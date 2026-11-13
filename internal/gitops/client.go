package gitops

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Config is explicit: neither ambient kubeconfig nor in-cluster environment
// discovery is used. The chart supplies a projected, rotating API token.
type Config struct {
	Server      string   `json:"server"`
	Namespace   string   `json:"namespace"`
	Workspace   string   `json:"workspace"`
	CAFile      string   `json:"ca_file"`
	TokenFile   string   `json:"token_file"`
	PollSeconds int      `json:"poll_seconds"`
	SecretNames []string `json:"secret_names"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	data, err := readBoundedFile(path, 16384)
	if err != nil {
		return config, errors.New("could not open GitOps configuration")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF {
		return config, errors.New("invalid GitOps configuration")
	}
	return config, config.validate()
}

func (c Config) validate() error {
	u, err := url.Parse(c.Server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		!namePattern.MatchString(c.Namespace) || !namePattern.MatchString(c.Workspace) ||
		c.CAFile == "" || c.TokenFile == "" || c.PollSeconds < 5 || c.PollSeconds > 300 || len(c.SecretNames) > 8 {
		return errors.New("GitOps requires an HTTPS API origin, namespace, workspace, CA/token files, and a 5–300 second poll interval")
	}
	seen := map[string]bool{}
	for _, name := range c.SecretNames {
		if !namePattern.MatchString(name) || seen[name] {
			return errors.New("invalid GitOps Secret allowlist")
		}
		seen[name] = true
	}
	return nil
}

type Client struct {
	config Config
	http   *http.Client
}

func NewClient(config Config) (*Client, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	pem, err := readBoundedFile(config.CAFile, 1<<20)
	if err != nil {
		return nil, errors.New("could not read GitOps API CA")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid GitOps API CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	return &Client{config: config, http: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func readBoundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return data, nil
}

// No API error body, URL, or credential is included in returned errors.
func (c *Client) request(ctx context.Context, method, path string, body []byte, result any) error {
	token, err := readBoundedFile(c.config.TokenFile, 16384)
	if err != nil {
		return errors.New("could not read GitOps API token")
	}
	credential := strings.TrimSpace(string(token))
	if credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return errors.New("invalid GitOps API token")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.config.Server+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not create GitOps API request")
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("GitOps API request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitOps API returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("GitOps API response exceeds limit or could not be read")
	}
	if result != nil && json.Unmarshal(data, result) != nil {
		return errors.New("invalid GitOps API response")
	}
	return nil
}

func (c *Client) collection(resource string) string {
	return "/apis/" + APIVersion + "/namespaces/" + c.config.Namespace + "/" + resource
}

func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	var result Snapshot
	// A dedicated namespace has at most 100 objects of each kind. Refuse a
	// partial list instead of treating its missing entries as deletions.
	for _, resource := range []string{"ticketconnections", "ticketprocedures"} {
		var list struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		if err := c.request(ctx, http.MethodGet, c.collection(resource)+"?limit=100", nil, &list); err != nil {
			return result, err
		}
		if list.Items == nil || len(list.Items) > 100 || list.Metadata.Continue != "" {
			return result, errors.New("GitOps resource list is incomplete or exceeds 100 objects")
		}
		seen := map[string]bool{}
		for _, raw := range list.Items {
			var object struct {
				APIVersion, Kind string
				Metadata         Metadata
			}
			if json.Unmarshal(raw, &object) != nil || object.APIVersion != APIVersion ||
				object.Metadata.Namespace != c.config.Namespace || !namePattern.MatchString(object.Metadata.Name) ||
				object.Metadata.UID == "" || object.Metadata.ResourceVersion == "" || object.Metadata.Generation < 1 || seen[object.Metadata.Name] {
				return result, errors.New("invalid GitOps resource metadata")
			}
			seen[object.Metadata.Name] = true
			if resource == "ticketconnections" {
				var item Connection
				if object.Kind != "TicketConnection" || json.Unmarshal(raw, &item) != nil {
					return result, errors.New("invalid TicketConnection")
				}
				if item.Spec.Workspace == c.config.Workspace && item.Metadata.DeletionTimestamp == nil {
					result.Connections = append(result.Connections, item)
				}
			} else {
				var item Procedure
				if object.Kind != "TicketProcedure" || json.Unmarshal(raw, &item) != nil {
					return result, errors.New("invalid TicketProcedure")
				}
				if item.Spec.Workspace == c.config.Workspace && item.Metadata.DeletionTimestamp == nil {
					result.Procedures = append(result.Procedures, item)
				}
			}
		}
	}
	if len(result.Connections) > 8 {
		return result, errors.New("GitOps supports at most eight connections per workspace")
	}
	return result, nil
}

var secretKey = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,253}$`)

func (c *Client) Token(ctx context.Context, ref *SecretRef) (string, error) {
	if ref == nil {
		return "", nil
	}
	allowed := false
	for _, name := range c.config.SecretNames {
		if name == ref.Name {
			allowed = true
		}
	}
	if !allowed || !secretKey.MatchString(ref.Key) {
		return "", errors.New("Secret reference is not allowed")
	}
	var secret struct {
		Metadata Metadata          `json:"metadata"`
		Data     map[string][]byte `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/v1/namespaces/"+c.config.Namespace+"/secrets/"+ref.Name, nil, &secret); err != nil {
		return "", err
	}
	token := secret.Data[ref.Key]
	if secret.Metadata.Name != ref.Name || secret.Metadata.Namespace != c.config.Namespace || len(token) == 0 || len(token) > 8192 {
		return "", errors.New("Secret key is missing or invalid")
	}
	return string(token), nil
}

func (c *Client) SetStatus(ctx context.Context, object Connection, status Status) error {
	// Both preconditions prevent reporting an older spec (or a deleted and
	// recreated object) as reconciled. Conflicts are retried on the next poll.
	body, _ := json.Marshal(map[string]any{"metadata": map[string]string{
		"uid": object.Metadata.UID, "resourceVersion": object.Metadata.ResourceVersion}, "status": status})
	return c.request(ctx, http.MethodPatch, c.collection("ticketconnections")+"/"+object.Metadata.Name+"/status", body, nil)
}
