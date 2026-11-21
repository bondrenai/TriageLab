package ticketflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

var supportedAttachmentExtensions = []string{
	".py", ".json", ".log", ".txt",
	".yaml", ".yml", ".toml", ".csv", ".tsv", ".xml", ".ini", ".conf", ".cfg", ".md",
	".go", ".js", ".ts", ".jsx", ".tsx", ".rb", ".rs", ".java", ".sql", ".diff", ".patch",
}

var attachmentStem = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,100}$`)

func supportedAttachmentName(name string) bool {
	ext := path.Ext(name)
	return slices.Contains(supportedAttachmentExtensions, ext) &&
		attachmentStem.MatchString(strings.TrimSuffix(name, ext))
}

var codeFenceExtensions = map[string]string{
	"py": ".py", "python": ".py", "python3": ".py",
	"json": ".json", "jsonc": ".json",
	"log": ".log",
	"txt": ".txt", "text": ".txt", "plaintext": ".txt",
	"yaml": ".yaml", "yml": ".yml",
	"toml": ".toml",
	"csv":  ".csv", "tsv": ".tsv",
	"xml": ".xml",
	"ini": ".ini", "conf": ".conf", "cfg": ".cfg",
	"md": ".md", "markdown": ".md",
	"go": ".go", "golang": ".go",
	"js": ".js", "javascript": ".js",
	"ts": ".ts", "typescript": ".ts",
	"jsx": ".jsx", "tsx": ".tsx",
	"rb": ".rb", "ruby": ".rb",
	"rs": ".rs", "rust": ".rs",
	"java":  ".java",
	"sql":   ".sql",
	"diff":  ".diff",
	"patch": ".patch",
}

const maxInlineCodeBlocksPerComment = 8

type EvidenceRule struct {
	Kind         string `json:"kind"`
	Match        string `json:"match"`
	RequiredWhen string `json:"required_when,omitempty"`
	Required     bool   `json:"required"`
	Pattern      string `json:"pattern,omitempty"`
	Question     string `json:"question,omitempty"`
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
		if (rule.RequiredWhen != "" && !rule.Required) || !validText(rule.Match, 100) || ((rule.Required || rule.Question != "") && !validText(rule.Question, 500)) {
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
			if rule.Pattern != "" || (!supportedAttachmentName(rule.Match) && !slices.Contains(supportedAttachmentExtensions, rule.Match)) {
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

// extractCodeBlocks parses Markdown fenced code blocks from a requester reply
// into bounded inline attachments. Only blocks with an explicit supported
// filename or a recognized language tag are returned.
func extractCodeBlocks(body string) []attachment {
	if !strings.Contains(body, "```") && !strings.Contains(body, "~~~") {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var blocks []attachment
	usedNames := make(map[string]bool)
	inFence := false
	var fenceChar byte
	fenceLen := 0
	var blockName string
	var indent int
	var oversized bool
	var content strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		padding := len(line) - len(trimmed)
		if !inFence {
			if padding > 3 || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
				continue
			}
			ch := trimmed[0]
			n := 0
			for n < len(trimmed) && trimmed[n] == ch {
				n++
			}
			if n < 3 {
				continue
			}
			info := strings.TrimSpace(trimmed[n:])
			// Backtick info strings cannot contain backticks per CommonMark.
			if ch == '`' && strings.ContainsRune(info, '`') {
				continue
			}
			inFence = true
			fenceChar = ch
			fenceLen = n
			indent = padding
			oversized = false
			blockName = resolveCodeBlockName(info, len(blocks)+1, usedNames)
			content.Reset()
			continue
		}
		if padding <= 3 && len(trimmed) >= fenceLen && trimmed[0] == fenceChar {
			n := 0
			for n < len(trimmed) && trimmed[n] == fenceChar {
				n++
			}
			if n >= fenceLen && strings.TrimSpace(trimmed[n:]) == "" {
				inFence = false
				text := content.String()
				if blockName != "" && !oversized && strings.TrimSpace(text) != "" {
					usedNames[blockName] = true
					blocks = append(blocks, attachment{
						Name:   blockName,
						Size:   int64(len(text)),
						Inline: text,
					})
					if len(blocks) == maxInlineCodeBlocksPerComment {
						return blocks
					}
				}
				continue
			}
		}
		if blockName != "" && !oversized {
			line = line[min(indent, padding):]
			oversized = len(line)+1 > casefile.MaxFileBytes-content.Len()
			if !oversized {
				content.WriteString(line)
				content.WriteByte('\n')
			}
		}
	}
	return blocks
}

func resolveCodeBlockName(info string, index int, used map[string]bool) string {
	if info == "" {
		return ""
	}
	fields := strings.Fields(info)
	// First check for explicit filename=..., file=..., title=..., then a bare supported filename.
	for _, field := range fields {
		key, value, _ := strings.Cut(field, "=")
		switch strings.ToLower(key) {
		case "filename", "file", "title", "name":
			name := reproductionName(strings.Trim(value, `"'`))
			if supportedAttachmentName(name) && !used[name] {
				return name
			}
			return ""
		}
	}
	name := reproductionName(fields[0])
	if supportedAttachmentName(name) {
		if !used[name] {
			return name
		}
		return ""
	}
	lang := strings.ToLower(strings.TrimLeft(fields[0], "."))
	ext, ok := codeFenceExtensions[lang]
	if !ok {
		return ""
	}
	for n := index; n < index+maxInlineCodeBlocksPerComment+1; n++ {
		name := fmt.Sprintf("snippet-%d%s", n, ext)
		if !used[name] {
			return name
		}
	}
	return ""
}

// The first matching required attachment rule anchors the evidence bundle.
// Another rule may supply files from a separate requester reply if the
// selected bundle lacks its match. Older versions of the bundle are ignored.
// Real uploaded attachments always take precedence; inline Markdown code blocks
// fill in only when no uploaded attachment satisfies a rule.
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
	if primary != nil {
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
	}
	// Fall back to inline Markdown fenced code blocks for any attachment rule
	// that was not satisfied by an uploaded file.
	inlineByReply := make(map[int][]attachment)
	seenNames := make(map[string]bool, len(files))
	for _, f := range files {
		seenNames[reproductionName(f.Name)] = true
	}
	for _, r := range rules {
		if r.Kind != "attachment" || slices.ContainsFunc(files, func(f evidenceFile) bool { return r.matchesFile(f.attachment) }) {
			continue
		}
		for index, m := range slices.Backward(replies) {
			blocks, parsed := inlineByReply[index]
			if !parsed {
				blocks = extractCodeBlocks(m.Body)
				inlineByReply[index] = blocks
			}
			found := false
			for _, b := range blocks {
				if r.matchesFile(b) && !seenNames[reproductionName(b.Name)] {
					files = append(files, evidenceFile{b, m.ID, m.URL})
					seenNames[reproductionName(b.Name)] = true
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

// Older plans retain their digest when no evidence requirement uses CEL.
func conditionalEvidenceDigest(saved, effective EvidenceRules) string {
	if slices.ContainsFunc(saved, func(rule EvidenceRule) bool { return rule.RequiredWhen != "" }) {
		return digest(effective)
	}
	return ""
}
