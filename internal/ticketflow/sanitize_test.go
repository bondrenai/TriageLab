package ticketflow

import (
	"strings"
	"testing"
)

func TestOutboundTermsCollectRequestersAndInternalLines(t *testing.T) {
	cases := []Case{
		{TicketID: 7, Requester: &Requester{Name: "Rival Customer", Email: "rival@example.test"}},
		{TicketID: 2, Requester: &Requester{Name: "An", Email: "x@y"}},
		{TicketID: 3},
	}
	comments := []comment{
		{ID: 1, Public: true, Body: "a public requester line that must not become a term"},
		{ID: 2, Public: false, Body: "Expected: secret internal detail\nshort\nActual: error"},
	}
	terms := outboundTerms(cases, 1, comments)
	var values []string
	for _, term := range terms {
		values = append(values, term.value)
	}
	want := []string{"rival customer", "rival@example.test", "expected: secret internal detail", "actual: error"}
	if strings.Join(values, "|") != strings.Join(want, "|") {
		t.Fatalf("terms = %v, want %v", values, want)
	}
}

func TestSanitizeOutputHoldsDisclosures(t *testing.T) {
	terms := outboundTerms([]Case{{TicketID: 9, Requester: &Requester{Name: "Rival Customer", Email: "rival@example.test"}}}, 1,
		[]comment{{ID: 5, Public: false, Body: "Expected: secret internal detail"}})
	cases := []struct {
		name    string
		body    string
		secrets []string
		want    string
	}{
		{"clean", "To help us reproduce the timeout, please reply with the following:", nil, ""},
		{"other requester name", "Rival Customer saw the same timeout.", nil, "another tracked requester's identity"},
		{"other requester email", "Please contact RIVAL@Example.Test.", nil, "another tracked requester's identity"},
		{"internal note line", "Details: expected: secret internal detail", nil, "internal note text"},
		{"configured credential", "The key is test-ticket-credential.", []string{"test-ticket-credential"}, "credential-like content"},
		{"credential pattern", "Use ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA here.", nil, "credential-like content"},
		{"unsafe characters", "broken \x1b[31mtext\x1b[0m", nil, "credential-like content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := sanitizeOutput(tc.body, tc.secrets, terms)
			if tc.want == "" {
				if len(findings) != 0 {
					t.Fatalf("clean text was held: %v", findings)
				}
				return
			}
			if len(findings) != 1 || !strings.Contains(findings[0], tc.want) {
				t.Fatalf("findings = %v, want one containing %q", findings, tc.want)
			}
		})
	}
}

func TestSanitizeOutputReportsEachKindOnce(t *testing.T) {
	terms := outboundTerms([]Case{
		{TicketID: 8, Requester: &Requester{Name: "Rival Customer", Email: "rival@example.test"}},
		{TicketID: 9, Requester: &Requester{Name: "Third Customer"}},
	}, 1, nil)
	findings := sanitizeOutput("Rival Customer and Third Customer and rival@example.test", nil, terms)
	if len(findings) != 1 || !strings.Contains(findings[0], "another tracked requester's identity") {
		t.Fatalf("findings = %v, want a single requester-identity finding", findings)
	}
}
