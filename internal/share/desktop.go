package share

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Notice is a desktop notification. The first action also runs when the
// notification itself is clicked.
type Notice struct {
	Title, Body string
	// Image is a picture to show with it, such as the received photo.
	Image   string
	Actions []Action
}

// Action is a button on a notification.
type Action struct {
	Label string
	Run   func()
	// URI is what the button opens where notifications cannot call back
	// into Remoter (Windows toasts): a file:// or web address.
	URI string
}

// Desktop turns what arrives into notifications, clipboard text and opened
// links on this computer.
type Desktop struct{}

// Files tells the user about received files.
func (Desktop) Files(from string, paths []string) {
	if len(paths) == 0 {
		return
	}
	dir := filepath.Dir(paths[0])
	n := Notice{Actions: []Action{{"Show in folder", func() { ShowInFolder(paths[0]) }, fileURI(dir)}}}
	if len(paths) == 1 {
		p := paths[0]
		n.Title = fmt.Sprintf("%s from %s", kind(p), from)
		n.Body = filepath.Base(p) + " saved to " + shortDir(dir)
		if kind(p) == "Photo" {
			n.Image = p
		}
		n.Actions = append([]Action{{"Open", func() { Open(p) }, fileURI(p)}}, n.Actions...)
	} else {
		n.Title = fmt.Sprintf("%d files from %s", len(paths), from)
		names := make([]string, 0, 3)
		for _, p := range paths[:min(3, len(paths))] {
			names = append(names, filepath.Base(p))
		}
		if len(paths) > 3 {
			names = append(names, fmt.Sprintf("and %d more", len(paths)-3))
		}
		n.Body = strings.Join(names, ", ") + " saved to " + shortDir(dir)
	}
	Notify(n)
}

// Text puts received text on the clipboard; a link can also be opened.
func (Desktop) Text(from, text string) {
	copied := CopyText(text) == nil
	if IsLink(text) {
		body := text
		if copied {
			body += "\n(copied)"
		}
		Notify(Notice{Title: "Link from " + from, Body: body, Actions: []Action{{"Open", func() { Open(text) }, text}}})
		return
	}
	preview := text
	if r := []rune(preview); len(r) > 200 {
		preview = string(r[:200]) + "…"
	}
	title := "Text from " + from
	if copied {
		title = "Copied text from " + from
	}
	Notify(Notice{Title: title, Body: preview})
}

func kind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".bmp":
		return "Photo"
	case ".mp4", ".mov", ".mkv", ".webm", ".3gp":
		return "Video"
	case ".pdf":
		return "PDF"
	}
	return "File"
}

func shortDir(dir string) string {
	if home, err := userHome(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return dir
}

func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x -> /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
