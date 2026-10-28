//go:build linux || windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// statSecond is one second of a session, for the diagnostics report.
type statSecond struct {
	at              time.Time
	frames          int
	bytes           int
	decodes         int
	decode, decMax  time.Duration
	renders         int
	render, rendMax time.Duration
	codec           string
}

// timings collects decode and render durations for the current second.
type timings struct {
	decodes, renders int
	decode, render   time.Duration
	decMax, rendMax  time.Duration
}

func (t *timings) addDecode(d time.Duration) {
	t.decodes++
	t.decode += d
	t.decMax = max(t.decMax, d)
}

func (t *timings) addRender(d time.Duration) {
	t.renders++
	t.render += d
	t.rendMax = max(t.rendMax, d)
}

func avgMs(total time.Duration, n int) float64 {
	if n == 0 {
		return 0
	}
	return total.Seconds() * 1000 / float64(n)
}

func ms(d time.Duration) float64 { return d.Seconds() * 1000 }

// diagnosticsReport puts the viewer's side above what the host sent.
func (a *app) diagnosticsReport(host string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== Remoter desktop viewer %s (%s/%s, %d CPUs)\n", version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	fmt.Fprintf(&b, "display: %s, output %dx%d, scale %.2f\n", sdl.GetCurrentVideoDriver(), a.out.X, a.out.Y, a.scale)
	dec := "none (JPEG tiles only)"
	if a.dec != nil {
		dec = fmt.Sprintf("%T", a.dec)
	}
	url := ""
	if a.client != nil {
		url = a.client.URL
	}
	fmt.Fprintf(&b, "server: %s (%s), decoder: %s, video size %dx%d, keyboard captured: %v\n", a.server, url, dec, a.videoSize.X, a.videoSize.Y, a.captured)
	if a.noVideo != "" {
		fmt.Fprintf(&b, "host says no video: %s\n", a.noVideo)
	}
	if a.quality != "" {
		fmt.Fprintf(&b, "picture: %s at %d%% (asked for %q)\n", a.quality, a.qualityScale, a.cfg.Quality)
	}
	if a.linkResult != "" {
		b.WriteString(a.linkResult + "\n")
	}
	b.WriteString("last seconds (fps, Mbit/s, decode avg/max ms, render avg/max ms, codec):\n")
	for _, s := range a.history {
		fmt.Fprintf(&b, "  %s %3d fps %6.2f Mbit/s  dec %5.1f/%5.1f  draw %5.1f/%5.1f  %s\n",
			s.at.Format("15:04:05"), s.frames, float64(s.bytes)*8/1e6,
			avgMs(s.decode, s.decodes), ms(s.decMax), avgMs(s.render, s.renders), ms(s.rendMax), s.codec)
	}
	b.WriteString("\n== Host\n")
	b.WriteString(host)
	return b.String()
}

// finishDiagnostics copies the report once the host's part and the link
// test are both in, or as much as there is after a while.
func (a *app) finishDiagnostics(now time.Time) {
	select {
	case r := <-a.linkTest:
		a.linkResult, a.linkTest = r, nil
	default:
	}
	waited := now.Sub(a.diagAsked)
	if a.diagHost == nil && waited <= 20*time.Second || a.linkTest != nil && waited <= 20*time.Second {
		return
	}
	if a.linkTest != nil {
		a.linkResult = "link test: did not finish in time"
	}
	host := "(no answer: the host's build is too old to send diagnostics)\n"
	if a.diagHost != nil {
		host = *a.diagHost
	}
	a.diagHost, a.linkTest = nil, nil
	a.copyDiagnostics(host)
}

// linkTest measures the connection to the relay at base (http://host:port): ping round
// trips and a download, as one line for the report.
func linkTest(base string) string {
	client := &http.Client{Timeout: 15 * time.Second}
	var lo, hi time.Duration
	var pings int
	for range 5 {
		start := time.Now()
		resp, err := client.Get(base + "/ping")
		if err != nil {
			return "link test: ping failed: " + err.Error()
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
		resp.Body.Close()
		d := time.Since(start)
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "pong" {
			return "link test: the host's build is too old to test the link"
		}
		if pings == 0 || d < lo {
			lo = d
		}
		hi = max(hi, d)
		pings++
	}
	line := fmt.Sprintf("link test: ping %.1f-%.1f ms", ms(lo), ms(hi))
	const mb = 16
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf("%s/speedtest?mb=%d", base, mb))
	if err != nil {
		return line + ", download failed: " + err.Error()
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	secs := time.Since(start).Seconds()
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("%s, download failed after %d bytes (%s)", line, n, resp.Status)
	}
	return fmt.Sprintf("%s, download %.1f Mbit/s (%d MiB in %.1f s)", line, float64(n)*8/1e6/secs, n>>20, secs)
}

// qualityLabel is how a quality setting reads in the title and notices.
func qualityLabel(q string) string {
	switch q {
	case "", "auto":
		return "Auto"
	case "sharp":
		return "Sharp"
	case "balanced":
		return "Balanced"
	case "smooth":
		return "Smooth"
	}
	if n, err := strconv.Atoi(q); err == nil {
		return strconv.Itoa(n) + "%"
	}
	return q // a setting from a newer host: show it as it is
}

// qualityText is the setting with the scale the host sends at, when the
// setting does not already say it.
func qualityText(q string, scale int) string {
	label := qualityLabel(q)
	if _, err := strconv.Atoi(q); err == nil || scale <= 0 || scale == 100 {
		return label
	}
	return fmt.Sprintf("%s %d%%", label, scale)
}

// copyDiagnostics puts the combined report on the local clipboard.
func (a *app) copyDiagnostics(host string) {
	a.diagAsked = time.Time{}
	report := a.diagnosticsReport(host)
	a.clip = report // so it is not sent to the host as a clipboard change
	sdl.SetClipboardText(report)
	a.showNotice("Diagnostics copied to the clipboard  ·  paste them into the chat", 5*time.Second)
}
