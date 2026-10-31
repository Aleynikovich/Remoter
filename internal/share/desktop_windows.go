//go:build windows

package share

import (
	"encoding/xml"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/update"
)

func userHome() (string, error) { return os.UserHomeDir() }

// DownloadDir is the user's Downloads folder, wherever they moved it.
func DownloadDir() string {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// toastScript shows the toast XML in REMOTER_TOAST. It borrows PowerShell's
// app identity, which Windows always lets show notifications.
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml($env:REMOTER_TOAST)
$t = New-Object Windows.UI.Notifications.ToastNotification $x
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($t)`

// Notify shows n as a Windows toast. Its buttons open their URI.
func Notify(n Notice) {
	esc := func(s string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(s))
		return strings.ReplaceAll(b.String(), `"`, "&quot;")
	}
	var x strings.Builder
	x.WriteString(`<toast`)
	if len(n.Actions) > 0 && n.Actions[0].URI != "" {
		x.WriteString(` activationType="protocol" launch="` + esc(n.Actions[0].URI) + `"`)
	}
	x.WriteString(`><visual><binding template="ToastGeneric"><text>` + esc(n.Title) + `</text><text>` + esc(n.Body) + `</text>`)
	if n.Image != "" {
		x.WriteString(`<image placement="hero" src="` + esc(fileURI(n.Image)) + `"/>`)
	}
	x.WriteString(`</binding></visual>`)
	if len(n.Actions) > 0 {
		x.WriteString(`<actions>`)
		for _, a := range n.Actions {
			if a.URI != "" {
				x.WriteString(`<action content="` + esc(a.Label) + `" activationType="protocol" arguments="` + esc(a.URI) + `"/>`)
			}
		}
		x.WriteString(`</actions>`)
	}
	x.WriteString(`</toast>`)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "REMOTER_TOAST="+x.String())
	update.HideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("share: cannot show a notification (%v %s): %s: %s", err, strings.TrimSpace(string(out)), n.Title, n.Body)
	}
}

// CopyText puts text on the Windows clipboard.
func CopyText(text string) error {
	c := clipboard.New()
	if c == nil {
		return errors.New("no clipboard")
	}
	return c.WriteText(text)
}

// ReadClipboard returns the clipboard text.
func ReadClipboard() (string, error) {
	c := clipboard.New()
	if c == nil {
		return "", errors.New("no clipboard")
	}
	return c.ReadText()
}

// Open opens a file or link with its default app.
func Open(target string) {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target)
	if err := cmd.Start(); err != nil {
		log.Printf("share: cannot open %s: %v", target, err)
	}
}

// ShowInFolder opens Explorer with path selected.
func ShowInFolder(path string) {
	exec.Command("explorer.exe", "/select,", path).Start()
}
