// Package diag keeps the most recent log lines so viewers can ask for them.
package diag

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
)

// Lines is how many recent log lines Log keeps. It also keeps the first
// StartLines, which say how the host started (which encoder, which capture).
const (
	Lines      = 300
	StartLines = 40
)

// Log holds the most recent log output. Add it to the log package's output
// with io.MultiWriter.
var Log = &Ring{}

// Ring is an io.Writer that keeps the last Lines lines written to it.
type Ring struct {
	mu      sync.Mutex
	first   []string
	lines   []string
	dropped int
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
		if line := string(r.partial[:i]); len(r.first) < StartLines {
			r.first = append(r.first, line)
		} else {
			r.lines = append(r.lines, line)
		}
		r.partial = r.partial[i+1:]
	}
	if over := len(r.lines) - Lines; over > 0 {
		r.lines = append(r.lines[:0], r.lines[over:]...)
		r.dropped += over
	}
	return len(p), nil
}

// String returns the kept lines, oldest first.
func (r *Ring) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.first
	if r.dropped > 0 {
		all = append(all[:len(all):len(all)], fmt.Sprintf("... %d lines left out ...", r.dropped))
	}
	return strings.Join(append(all[:len(all):len(all)], r.lines...), "\n")
}
