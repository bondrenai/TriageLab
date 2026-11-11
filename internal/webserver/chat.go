package webserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/resultchat"
)

const maxChatBytes = 2 << 20

type chatGenerator interface {
	Generate(context.Context, map[string]any, []resultchat.Message, string) (resultchat.Answer, error)
}

type chatMessage struct {
	ID        string                `json:"id"`
	Role      string                `json:"role"`
	Content   string                `json:"content"`
	CreatedAt string                `json:"created_at"`
	Citations []resultchat.Citation `json:"citations,omitempty"`
	Actor     string                `json:"actor,omitempty"`
}

type chatRecord struct {
	Version  int           `json:"version"`
	RunID    string        `json:"run_id"`
	Status   string        `json:"status"`
	Messages []chatMessage `json:"messages"`
	Error    string        `json:"error,omitempty"`
}

type chatView struct {
	chatRecord
	CanAsk            bool   `json:"can_ask"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	MaxMessageBytes   int    `json:"max_message_bytes"`
}

type chatJob struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

// EnableChat attaches a deliberately tool-free completion client. Discussion
// is stored separately and never updates report.json or investigator state.
func (s *Server) EnableChat(config resultchat.Config) error {
	client, err := resultchat.New(config)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.chat != nil {
		client.Close()
		return errors.New("chat is already configured or the workspace is closed")
	}
	s.chat, s.chatClose = client, client.Close
	if secret := os.Getenv(config.APIKeyEnv); secret != "" {
		s.secrets = append(s.secrets, secret)
	}
	return nil
}

func emptyChat(id string) chatRecord {
	return chatRecord{Version: 1, RunID: id, Status: "idle", Messages: []chatMessage{}}
}

func readChat(root *os.Root, id string) (chatRecord, error) {
	file, err := openRegular(root, "chat.json")
	if errors.Is(err, fs.ErrNotExist) {
		return emptyChat(id), nil
	}
	if err != nil {
		return chatRecord{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxChatBytes+1))
	if err != nil || len(data) > maxChatBytes {
		return chatRecord{}, errors.New("chat exceeds size limit")
	}
	var record chatRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if record.Version != 1 || record.RunID != id || len(record.Messages) > resultchat.MaxHistoryMessages {
		return record, errors.New("invalid chat record")
	}
	switch record.Status {
	case "idle", "answering", "error", "interrupted":
	default:
		return record, errors.New("invalid chat status")
	}
	seen := make(map[string]bool)
	for _, message := range record.Messages {
		limit := resultchat.MaxAnswerBytes
		if message.Role == "user" {
			limit = resultchat.MaxMessageBytes
		} else if message.Role != "assistant" {
			return record, errors.New("invalid chat role")
		}
		if len(message.Content) > limit || strings.TrimSpace(message.Content) == "" || !utf8.ValidString(message.Content) || len(message.Citations) > 32 {
			return record, errors.New("invalid chat message")
		}
		if message.ID == "" || len(message.ID) > 128 || seen[message.ID] || strings.IndexFunc(message.ID, unicode.IsControl) >= 0 {
			return record, errors.New("invalid chat message identity")
		}
		if _, err := time.Parse(time.RFC3339Nano, message.CreatedAt); err != nil {
			return record, errors.New("invalid chat message timestamp")
		}
		seen[message.ID] = true
		citations := make(map[resultchat.Citation]bool)
		for _, citation := range message.Citations {
			if message.Role != "assistant" || citation.Round < 1 || citation.Round > 32 || citation.ProcessID == "" || len(citation.ProcessID) > 256 || strings.IndexFunc(citation.ProcessID, unicode.IsControl) >= 0 || citations[citation] {
				return record, errors.New("invalid saved chat citation")
			}
			citations[citation] = true
		}
	}
	if record.Messages == nil {
		record.Messages = []chatMessage{}
	}
	return record, nil
}

func writeChat(root *os.Root, record chatRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxChatBytes {
		return errors.New("chat exceeds size limit")
	}
	temporary := ".chat-" + chatID() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := root.Rename(temporary, "chat.json"); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func chatID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b[:])
}

// A filesystem lock prevents duplicate generation if two workspace processes
// use the same report directory. It also distinguishes a dead job after restart.
func lockChat(root *os.Root) (*os.File, error) {
	f, err := root.OpenFile(".chat.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("invalid chat lock")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func releaseChat(lock *os.File) { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); _ = lock.Close() }

func terminalChatReport(saved map[string]any) bool {
	switch saved["status"] {
	case "completed", "error", "interrupted", "timed_out", "closed":
		return true
	}
	return false
}

// chatViewLocked requires s.mu, including while it inspects the durable journal.
func (s *Server) chatViewLocked(root *os.Root, saved map[string]any, record chatRecord, identity auth.Identity) chatView {
	view := chatView{chatRecord: record, MaxMessageBytes: resultchat.MaxMessageBytes}
	if record.Status == "answering" && (s.chatActive == nil || s.chatActive.id != record.RunID) {
		if lock, err := lockChat(root); err == nil {
			releaseChat(lock)
			view.Status, view.Error = "interrupted", "The workspace stopped before saving an answer. Your question is saved; no request was retried."
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			view.Status, view.Error = "error", "Could not verify whether an answer is still running."
			view.UnavailableReason = view.Error
		}
	}
	if message := s.chatErrors[record.RunID]; message != "" {
		view.Status, view.Error = "error", message
	}
	switch {
	case s.closed:
		view.UnavailableReason = "The workspace is shutting down."
	case !identity.Role.AtLeast(auth.RoleUser):
		view.UnavailableReason = "The user or admin role is required to ask questions."
	case s.chat == nil:
		view.UnavailableReason = "Results chat has not been configured for this workspace."
	case !terminalChatReport(saved) || s.activeID == record.RunID:
		view.UnavailableReason = "Questions are available after this investigation finishes or is closed."
	case view.Status == "answering":
		view.UnavailableReason = "An answer is being prepared."
	case s.chatActive != nil:
		view.UnavailableReason = "An answer is being prepared for another investigation."
	case len(record.Messages)+2 > resultchat.MaxHistoryMessages:
		view.UnavailableReason = "This conversation has reached its saved message limit."
	}
	view.CanAsk = view.UnavailableReason == ""
	return view
}

func (s *Server) chatRoute(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(w, 405, "Method not allowed")
		return
	}
	identity := actorFrom(r.Context())
	if !identity.Role.AtLeast(auth.RoleViewer) || (r.Method == http.MethodPost && !identity.Role.AtLeast(auth.RoleUser)) {
		fail(w, 403, "This workspace role cannot use this chat operation")
		return
	}
	var input struct {
		Message string `json:"message"`
	}
	if r.Method == http.MethodPost {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 6*resultchat.MaxMessageBytes+1024))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !utf8.ValidString(input.Message) || len(input.Message) > resultchat.MaxMessageBytes || strings.TrimSpace(input.Message) == "" {
			fail(w, 400, fmt.Sprintf("Use a nonempty question of at most %d UTF-8 bytes", resultchat.MaxMessageBytes))
			return
		}
		input.Message = report.Redact(strings.TrimSpace(input.Message), s.secrets...).(string)
		if input.Message == "" || len(input.Message) > resultchat.MaxMessageBytes {
			fail(w, 400, "The sanitized question is empty or too long")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail(w, 503, "The workspace is shutting down")
		return
	}
	root, err := s.reportRoot(id)
	if err != nil {
		fail(w, 404, "Report not found")
		return
	}
	defer func() {
		if root != nil {
			root.Close()
		}
	}()
	saved, err := s.readReport(id)
	if err != nil {
		fail(w, 404, "Report not found or unreadable")
		return
	}
	s.projectLifecycle(id, saved, s.activeID == id)
	record, err := readChat(root, id)
	if err != nil {
		fail(w, 500, "Could not read the saved conversation")
		return
	}
	view := s.chatViewLocked(root, saved, record, identity)
	if r.Method == http.MethodGet {
		jsonResponse(w, 200, report.Redact(view, s.secrets...))
		return
	}
	if !view.CanAsk {
		fail(w, 409, view.UnavailableReason)
		return
	}
	lock, err := lockChat(root)
	if err != nil {
		fail(w, 409, "The conversation is busy or could not be locked")
		return
	}
	// Re-read under the cross-process lock; another process may have saved an
	// answer between the first read and lock acquisition.
	record, err = readChat(root, id)
	if err != nil || len(record.Messages)+2 > resultchat.MaxHistoryMessages {
		releaseChat(lock)
		fail(w, 409, "The conversation changed or reached its message limit; refresh it")
		return
	}
	history := make([]resultchat.Message, 0, len(record.Messages))
	for _, message := range record.Messages {
		history = append(history, resultchat.Message{Role: message.Role, Content: report.Redact(message.Content, s.secrets...).(string)})
	}
	record.Messages = append(record.Messages, chatMessage{ID: chatID(), Role: "user", Content: input.Message, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Actor: identity.Username})
	record.Status, record.Error = "answering", ""
	if err := writeChat(root, record); err != nil {
		releaseChat(lock)
		fail(w, 500, "Could not save your question; no model request was started")
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 125*time.Second)
	job := &chatJob{id: id, cancel: cancel, done: make(chan struct{})}
	s.chatActive = job
	delete(s.chatErrors, id)
	jobRoot := root
	root = nil // the job owns the open root and lock until its final durable write
	go s.answerChat(ctx, job, jobRoot, lock, saved, history, input.Message, record)
	jsonResponse(w, 202, report.Redact(chatView{chatRecord: record, MaxMessageBytes: resultchat.MaxMessageBytes, UnavailableReason: "An answer is being prepared."}, s.secrets...))
}

func (s *Server) answerChat(ctx context.Context, job *chatJob, root *os.Root, lock *os.File, saved map[string]any, history []resultchat.Message, question string, record chatRecord) {
	answer, err := s.chat.Generate(ctx, saved, history, question)
	job.cancel()
	record.Status, record.Error = "idle", ""
	if err != nil {
		record.Status, record.Error = "error", "The model could not answer this question. No request was retried."
		if errors.Is(err, context.Canceled) {
			record.Status, record.Error = "interrupted", "The workspace stopped before saving an answer. No request was retried."
		}
	} else {
		content := report.Redact(answer.Content, s.secrets...).(string)
		if strings.TrimSpace(content) == "" || len(content) > resultchat.MaxAnswerBytes {
			record.Status, record.Error = "error", "The model returned an empty or oversized answer."
		} else {
			record.Messages = append(record.Messages, chatMessage{ID: chatID(), Role: "assistant", Content: content, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Citations: answer.Citations})
		}
	}
	writeErr := writeChat(root, record)
	releaseChat(lock)
	root.Close()
	s.mu.Lock()
	if writeErr != nil {
		if s.chatErrors == nil {
			s.chatErrors = map[string]string{}
		}
		s.chatErrors[job.id] = "The answer could not be saved. Your original question remains saved; no request was retried."
	}
	s.chatActive = nil
	close(job.done)
	s.mu.Unlock()
}
