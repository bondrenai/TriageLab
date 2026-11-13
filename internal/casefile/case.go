// Package casefile collects untrusted case material without executing it.
package casefile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/bondrenai/TriageLab/internal/secretpattern"
)

const (
	MaxFileBytes        = 1 << 20
	MaxTotalBytes       = 4 << 20
	MaxFiles            = 64
	MaxDescriptionBytes = 64 << 10
	maxDirectories      = 1024
	maxDirectoryDepth   = 32
)

// Attachment retains bytes for upload but includes only its inventory in reports.
type Attachment struct {
	Path      string `json:"path"`
	Content   []byte `json:"-"`
	SHA256    string `json:"sha256"`
	SizeBytes int    `json:"size_bytes"`
	SourceURL string `json:"source_url,omitempty"`
}

type Case struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Source      map[string]any `json:"source"`
	Environment map[string]any `json:"environment"`
	Attachments []Attachment   `json:"attachments"`
	Evidence    []string       `json:"evidence"`
}

// Connector is deliberately read-only; register future connectors explicitly.
type Connector interface {
	Ingest(ctx context.Context, reference string) (Case, error)
}

var excludedDirectories = map[string]bool{
	".git": true, ".venv": true, "venv": true, "node_modules": true,
	"__pycache__": true, ".pytest_cache": true, ".ruff_cache": true,
	"dist": true, "build": true,
}

var credentialDirectories = map[string]bool{
	".ssh": true, ".aws": true, ".kube": true, ".gnupg": true,
}

var credentialNames = map[string]bool{
	"credentials": true, "credentials.json": true, "credentials.yaml": true,
	"credentials.yml": true, "kubeconfig": true, "id_rsa": true, "id_dsa": true,
	"id_ecdsa": true, "id_ed25519": true, ".netrc": true, ".npmrc": true, ".pypirc": true,
}

// ErrLocation marks a failure to use the location an operator named: the
// repository directory, or the directory that contains a named file or log.
// Refusals of material found there never wrap it, so callers can tell a path
// or environment problem from a content refusal. Its messages are fixed text.
var ErrLocation = errors.New("cannot open the input location")

// secretContent uses the same credential shapes that report redaction removes.
func secretContent(content []byte) bool {
	return secretpattern.PrivateKey.Match(content) || secretpattern.KnownToken.Match(content)
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create case ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func credentialName(name string) bool {
	name = strings.ToLower(name)
	if credentialNames[name] || name == ".env" {
		return true
	}
	if strings.HasPrefix(name, ".env.") && !strings.HasSuffix(name, ".example") &&
		!strings.HasSuffix(name, ".sample") && !strings.HasSuffix(name, ".template") {
		return true
	}
	for _, suffix := range []string{".pem", ".p12", ".pfx", ".key"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return strings.HasPrefix(name, "service-account") || strings.HasPrefix(name, "service_account")
}

// descendNoLinks opens each component relative to an already held directory.
// Comparing inode identity closes the check/open race; os.Root confines resolution.
func descendNoLinks(base *os.Root, relative string) (*os.Root, error) {
	current, err := base.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			current.Close()
			return nil, fmt.Errorf("parent traversal is not accepted")
		}
		before, err := current.Lstat(component)
		if err != nil {
			current.Close()
			return nil, fmt.Errorf("cannot inspect input directory")
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			current.Close()
			return nil, fmt.Errorf("symlinks and non-directory path components are not accepted")
		}
		next, err := current.OpenRoot(component)
		current.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot open input directory")
		}
		after, err := next.Stat(".")
		if err != nil || !os.SameFile(before, after) {
			next.Close()
			return nil, fmt.Errorf("input directory changed while opening")
		}
		current = next
	}
	return current, nil
}

// rejectTraversal checks the name as given, before filepath.Clean can
// normalize a parent reference away.
func rejectTraversal(name string) error {
	for _, component := range strings.Split(filepath.ToSlash(name), "/") {
		if component == ".." {
			return fmt.Errorf("parent traversal is not accepted")
		}
	}
	return nil
}

// Input location policy: the operator chooses where inputs live, and the
// material there is untrusted. Links in the path that leads to a named input
// are followed once, when the kernel opens it, as for the report directory
// (/tmp and /var are links on macOS). The named repository or file itself, and
// everything below the repository, must not be a link; every later open is
// relative to a held directory and refuses links (readFile, descendNoLinks,
// and the repository walk).

// openRepository opens the named repository directory. Lstat on the cleaned
// name inspects the final component without following it (a trailing slash
// would follow it). The identity check binds the opened root to that
// inspection, so a link swapped in before the open is refused.
func openRepository(repo string) (*os.Root, error) {
	if err := rejectTraversal(repo); err != nil {
		return nil, err
	}
	name := filepath.Clean(repo)
	named, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect the repository directory")
	}
	if named.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("the repository directory is a symlink; name its target instead")
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("the repository is not a directory")
	}
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("cannot open the repository directory")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(named, opened) {
		root.Close()
		return nil, fmt.Errorf("the repository directory changed while opening")
	}
	return root, nil
}

// openContaining opens the directory that holds a named file. That directory
// is location, not material, so it needs no identity check; readFile refuses
// a final-component link and checks the file's own identity.
func openContaining(local string) (*os.Root, string, error) {
	if err := rejectTraversal(local); err != nil {
		return nil, "", err
	}
	name := filepath.Clean(local)
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return nil, "", fmt.Errorf("cannot open the directory containing an input file")
	}
	return root, filepath.Base(name), nil
}

func readFile(root *os.Root, relative string) ([]byte, error) {
	if credentialName(path.Base(relative)) {
		return nil, fmt.Errorf("potential credential file refused: %s", path.Base(relative))
	}
	parent, err := descendNoLinks(root, path.Dir(relative))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := path.Base(relative)
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect input file: %s", name)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("only regular files are accepted: %s", name)
	}
	if before.Size() > MaxFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", MaxFileBytes, name)
	}
	// No-follow and nonblocking prevent a final-component swap from following a
	// symlink or hanging on a FIFO. The supported development hosts are Unix.
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open input file: %s", name)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, fmt.Errorf("input file changed while opening: %s", name)
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read input file: %s", name)
	}
	if len(content) > MaxFileBytes {
		return nil, fmt.Errorf("file exceeds %d bytes: %s", MaxFileBytes, name)
	}
	if secretContent(content) {
		return nil, fmt.Errorf("potential secret detected in input: %s", name)
	}
	return content, nil
}

// Ingest reads bounded inputs. Repository paths are preserved; separately supplied
// files/logs go under files/ and logs/. Nothing is run or evaluated on the host.
func Ingest(description, repo string, files, logs []string) (Case, error) {
	if strings.TrimSpace(description) == "" {
		return Case{}, fmt.Errorf("a nonempty problem description is required")
	}
	if len(description) > MaxDescriptionBytes {
		return Case{}, fmt.Errorf("description exceeds %d bytes", MaxDescriptionBytes)
	}
	if secretContent([]byte(description)) {
		return Case{}, fmt.Errorf("potential secret detected in problem description")
	}
	id, err := newID()
	if err != nil {
		return Case{}, err
	}
	result := Case{
		ID: id, Description: strings.TrimSpace(description), Source: map[string]any{"kind": "local"},
		Environment: map[string]any{}, Attachments: []Attachment{}, Evidence: []string{},
	}
	totalBytes := 0
	destinations := map[string]bool{}
	add := func(root *os.Root, relative, destination string) error {
		if !fs.ValidPath(destination) || strings.Contains(destination, `\`) ||
			strings.IndexFunc(destination, unicode.IsControl) >= 0 {
			return fmt.Errorf("unsafe attachment destination")
		}
		if destinations[destination] {
			return fmt.Errorf("duplicate attachment destination: %s", destination)
		}
		if len(result.Attachments) >= MaxFiles {
			return fmt.Errorf("case exceeds %d files", MaxFiles)
		}
		content, err := readFile(root, relative)
		if err != nil {
			return err
		}
		totalBytes += len(content)
		if totalBytes > MaxTotalBytes {
			return fmt.Errorf("case exceeds %d total attachment bytes", MaxTotalBytes)
		}
		destinations[destination] = true
		sum := sha256.Sum256(content)
		result.Attachments = append(result.Attachments, Attachment{
			Path: destination, Content: content, SHA256: hex.EncodeToString(sum[:]), SizeBytes: len(content),
		})
		return nil
	}
	if repo != "" {
		root, err := openRepository(repo)
		if err != nil {
			return Case{}, fmt.Errorf("%w: %w", ErrLocation, err)
		}
		defer root.Close()
		result.Environment["repository_name"] = filepath.Base(filepath.Clean(repo))
		directories := 0
		err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("cannot enumerate input repository")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlinks are not accepted: %s", entry.Name())
			}
			if entry.IsDir() {
				if credentialDirectories[strings.ToLower(entry.Name())] {
					return fmt.Errorf("potential credential directory refused: %s", entry.Name())
				}
				if relative != "." && excludedDirectories[entry.Name()] {
					return fs.SkipDir
				}
				directories++
				if directories > maxDirectories || strings.Count(relative, "/") > maxDirectoryDepth {
					return fmt.Errorf("repository exceeds directory traversal limits")
				}
				return nil
			}
			return add(root, relative, relative)
		})
		if err != nil {
			return Case{}, err
		}
	}
	for _, group := range []struct {
		prefix string
		paths  []string
	}{{"files", files}, {"logs", logs}} {
		for _, local := range group.paths {
			root, name, err := openContaining(local)
			if err != nil {
				return Case{}, fmt.Errorf("%w: %w", ErrLocation, err)
			}
			destination := group.prefix + "/" + name
			err = add(root, name, destination)
			root.Close()
			if err != nil {
				return Case{}, err
			}
			if group.prefix == "logs" {
				result.Evidence = append(result.Evidence, "User-supplied log: "+destination)
			}
		}
	}
	return result, nil
}
