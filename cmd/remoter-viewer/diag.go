//go:build linux || windows

package main

import (
	"fmt"
	"runtime"
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
	fmt.Fprintf(&b, "server: %s, decoder: %s, video size %dx%d, keyboard captured: %v\n", a.server, dec, a.videoSize.X, a.videoSize.Y, a.captured)
	if a.noVideo != "" {
		fmt.Fprintf(&b, "host says no video: %s\n", a.noVideo)
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

// copyDiagnostics puts the combined report on the local clipboard.
func (a *app) copyDiagnostics(host string) {
	a.diagAsked = time.Time{}
	report := a.diagnosticsReport(host)
	a.clip = report // so it is not sent to the host as a clipboard change
	sdl.SetClipboardText(report)
	a.showNotice("Diagnostics copied to the clipboard  ·  paste them into the chat", 5*time.Second)
}
