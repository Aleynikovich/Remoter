// Package share sends files, links and text straight between your own
// devices on the Tailscale network, like KDE Connect: a phone shares a PDF
// to the PC, the PC sends a photo to the phone. Every device runs the same
// small HTTP API on the Remoter port:
//
//	GET  /share/info              {"name": "...", "os": "linux", "version": "..."}
//	POST /share/file?name=a.pdf   body = the file; saved to Downloads
//	POST /share/text              body = UTF-8 text or a link
//
// Senders name themselves in the X-Remoter-From header. A sender of several
// files numbers them with X-Remoter-Index (from 1) and X-Remoter-Count, so
// the receiver can say "5 files from phone" once instead of five times.
// Only requests from the tailnet (or this computer) are accepted.
package share

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Port is where every device listens, the same port as the Remoter viewer.
const Port = "8080"

// MaxText is the largest text or link accepted.
const MaxText = 1 << 20

const (
	headerFrom  = "X-Remoter-From"
	headerIndex = "X-Remoter-Index"
	headerCount = "X-Remoter-Count"
)

// Info is what GET /share/info returns.
type Info struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Version string `json:"version,omitempty"`
}

// Server receives files and text from other devices.
type Server struct {
	Name    string
	Version string
	// Dir is where received files are saved; it is created when needed.
	Dir string
	// Allow decides which requests may use the API.
	Allow func(*http.Request) bool
	// Files is called after a sender's last file has arrived, with every
	// file of that batch.
	Files func(from string, paths []string)
	// Text is called with each text or link that arrives.
	Text func(from, text string)

	mu      sync.Mutex
	batches map[string][]string // sender -> files of an unfinished batch
}

// Register adds the share API to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /share/info", s.guard(s.info))
	mux.HandleFunc("POST /share/file", s.guard(s.file))
	mux.HandleFunc("POST /share/text", s.guard(s.text))
}

func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Allow != nil && !s.Allow(r) {
			http.Error(w, "only for devices on your Tailscale network", http.StatusForbidden)
			return
		}
		// Browsers cannot add this header to a cross-site request without
		// asking first, and nothing here answers that, so a web page open on
		// some device cannot post files or clipboard text to this one.
		if r.Method != http.MethodGet && r.Header.Get(headerFrom) == "" {
			http.Error(w, "missing "+headerFrom+" header", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Info{Name: s.Name, OS: runtime.GOOS, Version: s.Version})
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	from := sender(r)
	name := SafeName(r.URL.Query().Get("name"))
	path, err := s.save(r.Body, name)
	if err != nil {
		log.Printf("share: receiving %q from %s failed: %v", name, from, err)
		http.Error(w, "could not save the file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("share: received %s from %s", path, from)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": filepath.Base(path)})

	index, _ := strconv.Atoi(r.Header.Get(headerIndex))
	count, _ := strconv.Atoi(r.Header.Get(headerCount))
	s.mu.Lock()
	if s.batches == nil {
		s.batches = map[string][]string{}
	}
	if index <= 1 {
		delete(s.batches, from)
	}
	batch := append(s.batches[from], path)
	done := count <= 1 || index >= count
	if done {
		delete(s.batches, from)
	} else {
		s.batches[from] = batch
	}
	s.mu.Unlock()
	if done && s.Files != nil {
		go s.Files(from, batch)
	}
}

func (s *Server) text(w http.ResponseWriter, r *http.Request) {
	from := sender(r)
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxText+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(b) > MaxText {
		http.Error(w, "text too long, send it as a file", http.StatusRequestEntityTooLarge)
		return
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	log.Printf("share: received %d bytes of text from %s", len(text), from)
	w.WriteHeader(http.StatusNoContent)
	if s.Text != nil {
		go s.Text(from, text)
	}
}

// save writes body next to its final name first, so a broken transfer never
// leaves a half file that looks complete.
func (s *Server) save(body io.Reader, name string) (string, error) {
	if s.Dir == "" {
		return "", errors.New("no folder to save to")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	part := filepath.Join(s.Dir, ".remoter-"+hex.EncodeToString(rnd[:])+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	// Pick a free name and claim it, retrying if another transfer took it.
	for i := 0; i < 1000; i++ {
		dst := filepath.Join(s.Dir, numbered(name, i))
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.Link(part, dst); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			// No hard links on this file system: plain rename.
			if err := os.Rename(part, dst); err != nil {
				os.Remove(part)
				return "", err
			}
			return dst, nil
		}
		os.Remove(part)
		return dst, nil
	}
	os.Remove(part)
	return "", errors.New("too many files with that name")
}

// numbered returns "a.pdf", "a (1).pdf", "a (2).pdf", ...
func numbered(name string, i int) string {
	if i == 0 {
		return name
	}
	ext := filepath.Ext(name)
	if strings.HasSuffix(strings.ToLower(name), ".tar"+strings.ToLower(ext)) {
		ext = name[len(name)-len(".tar")-len(ext):]
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
}

// SafeName turns a sender's file name into one that is safe to save here:
// no folders, no characters Windows refuses, never hidden or empty.
func SafeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return "shared file"
	}
	// Windows reserves these whatever the extension.
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = strings.ToValidUTF8(name[:200-len(ext)], "") + ext
	}
	return name
}

func sender(r *http.Request) string {
	from := strings.TrimSpace(r.Header.Get(headerFrom))
	if from == "" {
		host := r.RemoteAddr
		if i := strings.LastIndex(host, ":"); i > 0 {
			host = host[:i]
		}
		return strings.Trim(host, "[]")
	}
	if len(from) > 64 {
		from = from[:64]
	}
	return strings.ToValidUTF8(from, "")
}

// IsLink reports whether text is a single web link.
func IsLink(text string) bool {
	if strings.ContainsAny(text, " \t\r\n") {
		return false
	}
	l := strings.ToLower(text)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}
