package ticketflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

type EvidenceRule struct {
	Kind     string `json:"kind"`
	Match    string `json:"match"`
	Required bool   `json:"required"`
	Pattern  string `json:"pattern,omitempty"`
	Question string `json:"question,omitempty"`
}

type EvidenceRules []EvidenceRule

func defaultEvidence() EvidenceRules {
	return EvidenceRules{
		{Kind: "field", Match: "Python version", Pattern: `^3\.12(?:\.[0-9]{1,3})?$`, Required: true, Question: "Python version: Which Python 3.12 version runs the affected script?"},
		{Kind: "field", Match: "Expected", Required: true, Question: "Expected: What timeout or behavior did you expect?"},
		{Kind: "field", Match: "Actual", Required: true, Question: "Actual: What happened instead, including the exact error if available?"},
		{Kind: "attachment", Match: ".py", Required: true, Question: "Please attach the smallest Python script (.py) that shows the timeout. Remove secrets and customer data."},
	}
}

// Accept the old on-disk checklist when upgrading a workspace. Saving the
// procedure writes the editable rule list.
func (rules *EvidenceRules) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var old struct {
			Expected, Actual, Config, Log bool
		}
		if err := d.Decode(&old); err != nil {
			return err
		}
		r := defaultEvidence()
		r[1].Required, r[2].Required = old.Expected, old.Actual
		r = append(r, EvidenceRule{Kind: "attachment", Match: ".json", Required: old.Config, Question: "Please attach the JSON configuration (.json) used by the reproduction."})
		if old.Log {
			r = append(r, EvidenceRule{Kind: "attachment", Match: ".log", Required: true, Question: "Please attach the log (.log) alongside the Python reproduction."})
		}
		*rules = r
		return nil
	}
	var r []EvidenceRule
	if err := d.Decode(&r); err != nil {
		return err
	}
	if r == nil {
		return errors.New("evidence must be a rule list")
	}
	*rules = r
	return nil
}

func (rules EvidenceRules) valid(validText func(string, int) bool) bool {
	if rules == nil || len(rules) > 12 {
		return false
	}
	for i, rule := range rules {
		if !validText(rule.Match, 100) || ((rule.Required || rule.Question != "") && !validText(rule.Question, 500)) {
			return false
		}
		switch rule.Kind {
		case "field":
			if strings.Contains(rule.Match, ":") {
				return false
			}
			if rule.Pattern != "" {
				if _, err := regexp.Compile(rule.Pattern); err != nil || !validText(rule.Pattern, 120) {
					return false
				}
			}
		case "attachment":
			if rule.Pattern != "" || (!fileName.MatchString(rule.Match) && !slices.Contains([]string{".py", ".json", ".txt", ".log"}, rule.Match)) {
				return false
			}
		default:
			return false
		}
		for _, other := range rules[:i] {
			if other.Kind == rule.Kind && (other.Match == rule.Match || rule.Kind == "field" && strings.EqualFold(other.Match, rule.Match)) {
				return false
			}
		}
	}
	return true
}

func (r EvidenceRule) matchesFile(a attachment) bool {
	name := reproductionName(a.Name)
	if strings.HasPrefix(r.Match, ".") {
		return strings.HasSuffix(name, r.Match)
	}
	return name == r.Match
}

type evidenceFile struct {
	attachment
	commentID  int64
	commentURL string
}

// The first matching required attachment rule anchors the evidence bundle.
// Another rule may supply files from a separate requester reply if the
// selected bundle lacks its match. Older versions of the bundle are ignored.
func (rules EvidenceRules) selectFiles(replies []comment) []evidenceFile {
	var primary *EvidenceRule
	var files []evidenceFile
	for _, required := range []bool{true, false} {
		for _, r := range rules {
			if r.Kind != "attachment" || r.Required != required {
				continue
			}
			for _, m := range slices.Backward(replies) {
				if slices.ContainsFunc(m.Files, r.matchesFile) {
					primary = &r
					for _, a := range m.Files {
						files = append(files, evidenceFile{a, m.ID, m.URL})
					}
					break
				}
			}
			if primary != nil {
				break
			}
		}
		if primary != nil {
			break
		}
	}
	if primary == nil {
		return nil
	}
	for _, r := range rules {
		if r.Kind != "attachment" || slices.ContainsFunc(files, func(f evidenceFile) bool { return r.matchesFile(f.attachment) }) {
			continue
		}
		for _, m := range slices.Backward(replies) {
			if slices.ContainsFunc(m.Files, primary.matchesFile) {
				continue
			}
			found := false
			for _, a := range m.Files {
				if r.matchesFile(a) {
					files = append(files, evidenceFile{a, m.ID, m.URL})
					found = true
				}
			}
			if found {
				break
			}
		}
	}
	return files
}
