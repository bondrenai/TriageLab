package ticketflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const maxCases = 100

type store struct {
	dir       string
	lock      *os.File
	mu        sync.RWMutex
	cases     map[int64]Case
	procedure *ProcedureVersion
}

func openStore(dir, account string) (*store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("ticket state needs a private directory with mode 0700")
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("ticket state is already in use")
	}
	s := &store{dir: dir, lock: lock, cases: make(map[int64]Case)}
	if err := s.load(account); err != nil {
		lock.Close()
		return nil, err
	}
	if err := s.loadProcedure(account); err != nil {
		lock.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) load(account string) error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".evidence-") || strings.HasPrefix(entry.Name(), ".case-") {
			if err := os.RemoveAll(filepath.Join(s.dir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id, err := strconv.ParseInt(entry.Name()[:len(entry.Name())-5], 10, 64)
		if err != nil || id <= 0 || entry.Name() != filename(id) || !entry.Type().IsRegular() || len(s.cases) >= maxCases {
			return errors.New("invalid ticket state directory")
		}
		f, err := os.OpenFile(filepath.Join(s.dir, entry.Name()), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			f.Close()
			return errors.New("ticket state file is not private and regular")
		}
		var c Case
		d := json.NewDecoder(io.LimitReader(f, 64<<10))
		err = d.Decode(&c)
		if err == nil && d.Decode(new(any)) != io.EOF {
			err = errors.New("trailing state data")
		}
		f.Close()
		if err != nil || c.TicketID != id || c.Account != account {
			return errors.New("ticket state is invalid or belongs to another source account or repository")
		}
		s.cases[id] = c
	}
	return nil
}

func filename(id int64) string { return strconv.FormatInt(id, 10) + ".json" }

func (s *store) get(id int64) (Case, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cases[id]
	return c, ok
}

func (s *store) list() []Case {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Case, 0, len(s.cases))
	for _, c := range s.cases {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TicketID < out[j].TicketID })
	return out
}

func (s *store) save(c Case) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cases[c.TicketID]; !ok && len(s.cases) >= maxCases {
		return errors.New("ticket case limit reached")
	}
	data, err := json.Marshal(c)
	if err != nil || len(data) >= 64<<10 {
		return errors.New("ticket state exceeds storage limit")
	}
	committed, err := writePrivate(s.dir, filename(c.TicketID), data)
	if committed {
		s.cases[c.TicketID] = c
	}
	return err
}

// writePrivate reports whether the rename became visible, including when
// directory sync failed. Callers must stop effects after an uncertain commit.
func writePrivate(directory, name string, data []byte) (bool, error) {
	f, err := os.CreateTemp(directory, ".case-")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return false, err
	}
	if err = os.Rename(f.Name(), filepath.Join(directory, name)); err != nil {
		return false, err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return true, fmt.Errorf("sync ticket state: %w", err)
	}
	defer dir.Close()
	return true, dir.Sync()
}
