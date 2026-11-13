// Package secretpattern holds the credential shapes that ingestion refuses and
// reports redact, so the two cannot drift apart. Detection is best effort.
package secretpattern

import "regexp"

var (
	// PrivateKey spans a PEM private key from its BEGIN marker through its END
	// marker, or through the end of the text when the block is truncated, so a
	// replacement removes the whole key. It matches whenever a BEGIN marker
	// appears, which is all that detection needs.
	PrivateKey = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?(?:-----END (?:[A-Z ]+ )?PRIVATE KEY-----|\z)`)
	// KnownToken matches well-known provider token formats.
	KnownToken = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|sk-(?:proj-|ant-)?[A-Za-z0-9_-]{24,}|xox[baprs]-[A-Za-z0-9-]{20,})\b`)
)
