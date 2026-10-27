// Package diag keeps the most recent log lines so viewers can ask for them.
package diag

import (
	"bytes"
	"strings"
	"sync"
)

// Lines is how many log lines Log keeps.
const Lines = 300

// Log holds the most recent log output. Add it to the log package's output
// with io.MultiWriter.
var Log = &Ring{}

// Ring is an io.Writer that keeps the last Lines lines written to it.
type Ring struct {
	mu      sync.Mutex
	lines   []string
	partial []byte
}

func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		r.lines = append(r.lines, string(r.partial[:i]))
		r.partial = r.partial[i+1:]
	}
	if over := len(r.lines) - Lines; over > 0 {
		r.lines = append(r.lines[:0], r.lines[over:]...)
	}
	return len(p), nil
}

// String returns the kept lines, oldest first.
func (r *Ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}
