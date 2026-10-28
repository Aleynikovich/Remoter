package share

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":              "report.pdf",
		"../../etc/passwd":        "passwd",
		`C:\Users\a\photo.jpg`:    "photo.jpg",
		".bashrc":                 "bashrc",
		"":                        "shared file",
		"..":                      "shared file",
		"a<b>:c?.txt":             "a_b__c_.txt",
		"CON.txt":                 "_CON.txt",
		"line\nbreak.png":         "line_break.png",
		"trailing dots... ":       "trailing dots",
		"IMG_2026 10 27 (1).heic": "IMG_2026 10 27 (1).heic",
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	if n := SafeName(strings.Repeat("x", 300) + ".pdf"); len(n) > 200 || !strings.HasSuffix(n, ".pdf") {
		t.Errorf("long name not shortened: %d %q", len(n), n[len(n)-10:])
	}
}

func TestNumbered(t *testing.T) {
	for _, c := range [][3]string{{"a.pdf", "1", "a (1).pdf"}, {"a", "2", "a (2)"}, {"x.tar.gz", "1", "x (1).tar.gz"}} {
		i := int(c[1][0] - '0')
		if got := numbered(c[0], i); got != c[2] {
			t.Errorf("numbered(%q, %d) = %q, want %q", c[0], i, got, c[2])
		}
	}
}

func TestSendAndReceive(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var batches [][]string
	var texts []string
	got := make(chan struct{}, 10)
	s := &Server{Name: "pc", Dir: dir,
		Files: func(from string, paths []string) {
			mu.Lock()
			batches = append(batches, paths)
			mu.Unlock()
			got <- struct{}{}
		},
		Text: func(from, text string) {
			mu.Lock()
			texts = append(texts, from+": "+text)
			mu.Unlock()
			got <- struct{}{}
		},
	}
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	src := t.TempDir()
	a := filepath.Join(src, "doc.pdf")
	os.WriteFile(a, []byte("%PDF one"), 0o644)
	c := &Client{From: "phone"}
	ctx := context.Background()

	info, err := c.Info(ctx, ts.URL)
	if err != nil || info.Name != "pc" {
		t.Fatalf("info: %v %+v", err, info)
	}
	// Two files of one batch, the second with the same name.
	if err := c.SendFile(ctx, ts.URL, a, 1, 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SendFile(ctx, ts.URL, a, 2, 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SendText(ctx, ts.URL, "https://example.com/x"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("callbacks did not run")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 1 || len(batches[0]) != 2 {
		t.Fatalf("batches = %v", batches)
	}
	if filepath.Base(batches[0][1]) != "doc (1).pdf" {
		t.Errorf("second file saved as %s", batches[0][1])
	}
	b, _ := os.ReadFile(filepath.Join(dir, "doc.pdf"))
	if string(b) != "%PDF one" {
		t.Errorf("content = %q", b)
	}
	if len(texts) != 1 || texts[0] != "phone: https://example.com/x" {
		t.Errorf("texts = %v", texts)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".remoter-*"))
	if len(left) != 0 {
		t.Errorf("partial files left: %v", left)
	}

	// A browser page cannot set the sender header without a preflight.
	resp, err := http.Post(ts.URL+"/share/text", "text/plain", strings.NewReader("evil"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("post without sender: %s", resp.Status)
	}
}

func TestIsLink(t *testing.T) {
	if !IsLink("https://a.b/c?d") || IsLink("see https://a.b") || IsLink("ftp://x") {
		t.Error("IsLink wrong")
	}
}
