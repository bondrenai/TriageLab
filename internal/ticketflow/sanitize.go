package ticketflow

import (
	"strings"

	"github.com/bondrenai/TriageLab/internal/report"
)

// Two sanitizer boundaries surround the investigator. The input sanitizer
// delouses untrusted requester prose before it becomes case evidence, so the
// description, stored facts, and sandbox environment all carry the same
// redacted text; attachments and the composed description keep casefile's
// refusal checks and are never silently rewritten. The output sanitizer never
// rewrites: a clarification or investigation comment that may disclose a
// configured credential, another tracked requester's identity, or internal
// note text is held for operator review instead of being sent.

type denyTerm struct {
	kind  string
	value string
}

// outboundTerms lists text that must not reach this ticket's audience:
// identities of requesters from other tracked cases and substantial lines of
// this ticket's internal notes. Matching is best effort and intentionally
// errs toward blocking; common words or short values can hold a harmless
// draft for review.
func outboundTerms(cases []Case, current int64, comments []comment) []denyTerm {
	var terms []denyTerm
	for _, other := range cases {
		if other.TicketID == current || other.Requester == nil {
			continue
		}
		for _, identity := range []string{other.Requester.Name, other.Requester.Email} {
			value := outboundNormalized(identity)
			if len(value) < 3 || (strings.Contains(value, "@") && len(value) < 6) {
				continue
			}
			terms = append(terms, denyTerm{kind: "another tracked requester's identity", value: value})
		}
	}
	for _, note := range comments {
		if note.Public {
			continue
		}
		for _, line := range strings.Split(note.Body, "\n") {
			if value := outboundNormalized(line); len(value) >= 12 {
				terms = append(terms, denyTerm{kind: "internal note text", value: value})
			}
		}
	}
	return terms
}

// outboundNormalized lowercases text and collapses every whitespace run so
// formatting cannot hide a denied phrase.
func outboundNormalized(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// sanitizeOutput checks text bound for the ticket source and reports why it
// must be held for review rather than sent or rewritten.
func sanitizeOutput(body string, secrets []string, terms []denyTerm) []string {
	var findings []string
	if redacted, _ := report.Redacted(body, secrets...); redacted != body {
		findings = append(findings, "it contains credential-like content or unsafe characters")
	}
	normalized := outboundNormalized(body)
	held := map[string]bool{}
	for _, term := range terms {
		if term.value != "" && !held[term.kind] && strings.Contains(normalized, term.value) {
			held[term.kind] = true
			findings = append(findings, "it may disclose "+term.kind)
		}
	}
	return findings
}
