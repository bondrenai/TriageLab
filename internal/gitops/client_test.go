package gitops

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca, token := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("first-token"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Config{Server: server.URL, Namespace: "support", Workspace: "lab", CAFile: ca, TokenFile: token, PollSeconds: 5, SecretNames: []string{"ticket-token"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestSecretAllowlistRotationAndNoCredentialErrors(t *testing.T) {
	var auth []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.Path != "/api/v1/namespaces/support/secrets/ticket-token" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"name": "ticket-token", "namespace": "support"}, "data": map[string][]byte{"token": []byte("private-source-credential")}})
	})
	ctx := context.Background()
	if _, err := c.Token(ctx, &SecretRef{Name: "other-secret", Key: "token"}); err == nil || len(auth) != 0 {
		t.Fatal("read a Secret outside allowlist")
	}
	for i, want := range []string{"first-token", "rotated-token"} {
		if i == 1 {
			if err := os.WriteFile(c.config.TokenFile, []byte(want), 0600); err != nil {
				t.Fatal(err)
			}
		}
		token, err := c.Token(ctx, &SecretRef{Name: "ticket-token", Key: "token"})
		if err != nil || token != "private-source-credential" || auth[i] != "Bearer "+want {
			t.Fatalf("token rotation failed: %v", err)
		}
	}
	_, err := c.Token(ctx, &SecretRef{Name: "ticket-token", Key: "missing"})
	if err == nil || strings.Contains(err.Error(), "private-source-credential") {
		t.Fatal("missing key accepted or leaked credential")
	}
}

func TestSnapshotRejectsPartialListsAndWrongNamespace(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"continuation", `{"metadata":{"continue":"next"},"items":[]}`},
		{"missing items", `{"metadata":{}}`},
		{"wrong namespace", `{"items":[{"apiVersion":"triagelab.bondren.ai/v1alpha1","kind":"TicketConnection","metadata":{"name":"issues","namespace":"other","uid":"uid","generation":1,"resourceVersion":"1"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(test.body)) })
			if _, err := c.Snapshot(context.Background()); err == nil {
				t.Fatal("accepted incomplete or foreign snapshot")
			}
		})
	}
}

func TestClientRefusesRedirectAndDoesNotEchoAPIError(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			requests := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Location", "https://example.invalid")
				w.WriteHeader(code)
				_, _ = w.Write([]byte("private-error-body"))
			})
			_, err := c.Snapshot(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-error-body") || requests != 1 {
				t.Fatalf("unsafe error handling: %v (%d requests)", err, requests)
			}
		})
	}
}

func TestStatusUsesUIDAndResourceVersionPreconditions(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/apis/triagelab.bondren.ai/v1alpha1/namespaces/support/ticketconnections/issues/status" || r.Header.Get("Content-Type") != "application/merge-patch+json" {
			t.Errorf("unexpected status request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Metadata Metadata
			Status   Status
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Metadata.UID != "original" || body.Metadata.ResourceVersion != "42" || body.Status.ObservedGeneration != 3 {
			t.Error("status write lacks optimistic preconditions")
		}
		w.WriteHeader(http.StatusConflict)
	})
	object := Connection{Metadata: Metadata{Name: "issues", UID: "original", ResourceVersion: "42", Generation: 3}}
	if err := c.SetStatus(context.Background(), object, Status{ObservedGeneration: 3}); err == nil {
		t.Fatal("status conflict ignored")
	}
}

func TestExplicitConfigRequired(t *testing.T) {
	base := Config{Server: "https://kubernetes.default.svc", Namespace: "support", Workspace: "lab", CAFile: "ca", TokenFile: "token", PollSeconds: 30}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Server = "http://localhost:8001" }, func(c *Config) { c.Server = "https://user:secret@cluster" },
		func(c *Config) { c.Namespace = "../other" }, func(c *Config) { c.TokenFile = "" }, func(c *Config) { c.PollSeconds = 0 },
		func(c *Config) { c.SecretNames = []string{"ok", "../no"} },
	} {
		copy := base
		mutate(&copy)
		if copy.validate() == nil {
			t.Fatal("accepted unsafe configuration")
		}
	}
}
