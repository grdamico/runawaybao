// Package enforcement coordinates explicitly authorized, exact-lease operations.
package enforcement

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrJournal = errors.New("enforcement journal unavailable or requires reconciliation")

type entry struct {
	Run, Scope, Execution, Event, Operation, Lease, PolicyDigest, Stage string
}

type journal struct {
	file   *os.File
	used   map[string]bool
	events map[string]bool
}

func openJournal(path string) (*journal, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrJournal
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, ErrJournal
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, ErrJournal
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrJournal
	}
	j := &journal{file: f, used: make(map[string]bool), events: make(map[string]bool)}
	if err := j.load(); err != nil || dir.Sync() != nil {
		f.Close()
		return nil, ErrJournal
	}
	return j, nil
}

func (j *journal) load() error {
	info, err := j.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) ||
		info.Sys().(*syscall.Stat_t).Nlink != 1 ||
		syscall.Flock(int(j.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return ErrJournal
	}
	if info.Size() > 0 {
		var end [1]byte
		if _, err := j.file.ReadAt(end[:], info.Size()-1); err != nil || end[0] != '\n' {
			return ErrJournal
		}
	}
	states := make(map[string]entry)
	scan := bufio.NewScanner(j.file)
	for scan.Scan() {
		var e entry
		if json.Unmarshal(scan.Bytes(), &e) != nil || e.Lease == "" || e.Operation == "" {
			return ErrJournal
		}
		old := states[e.Operation]
		switch e.Stage {
		case "authorized":
			if old.Stage != "" {
				return ErrJournal
			}
		case "dispatch_intent", "acknowledged", "uncertain", "not_attempted":
			stage := e.Stage
			e.Stage = old.Stage
			if e != old || (stage == "dispatch_intent" && (old.Stage != "authorized" || j.used[e.Lease] || j.events[e.Event])) ||
				(stage != "dispatch_intent" && old.Stage != "dispatch_intent") {
				return ErrJournal
			}
			e.Stage = stage
			j.used[e.Lease] = true
			j.events[e.Event] = true
		default:
			return ErrJournal
		}
		states[e.Operation] = e
	}
	return scan.Err()
}

func (j *journal) append(e entry, stage string) error {
	e.Stage = stage
	b, err := json.Marshal(e)
	if err != nil {
		return ErrJournal
	}
	if _, err := j.file.Write(append(b, '\n')); err != nil || j.file.Sync() != nil {
		return ErrJournal
	}
	return nil
}
