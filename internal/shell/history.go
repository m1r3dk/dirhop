package shell

import (
	"os"
	"path/filepath"
	"strings"
)

const historyLimit = 1000

// fileHistory implements term.History (newest first) and persists to disk.
type fileHistory struct {
	path    string
	entries []string // oldest first
}

func loadHistory(path string) *fileHistory {
	h := &fileHistory{path: path}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				h.entries = append(h.entries, line)
			}
		}
	}
	h.trim()
	return h
}

func (h *fileHistory) Add(entry string) {
	if entry = strings.TrimSpace(entry); entry == "" || (len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry) {
		return
	}
	h.entries = append(h.entries, entry)
	h.trim()
}

func (h *fileHistory) Len() int { return len(h.entries) }

// At returns the idx-th most recent entry.
func (h *fileHistory) At(idx int) string { return h.entries[len(h.entries)-1-idx] }

func (h *fileHistory) trim() {
	if len(h.entries) > historyLimit {
		h.entries = h.entries[len(h.entries)-historyLimit:]
	}
}

func (h *fileHistory) save() {
	if h.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o700)
	_ = os.WriteFile(h.path, []byte(strings.Join(h.entries, "\n")+"\n"), 0o600)
}
