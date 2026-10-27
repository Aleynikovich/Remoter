# Remoter

An open-source remote desktop tool in the spirit of TeamViewer: share your
screen, let someone control it, and connect with a 9-digit ID and a password.

> Status: early prototype. Screen streaming works everywhere; mouse and
> keyboard control works on Windows (macOS and Linux are still view-only).
> See the milestone issues.

## How it works

```
 ┌──────────────┐   wss    ┌──────────────────┐   wss    ┌──────────────┐
 │ remoter-host │ ───────▶ │  remoter-server  │ ◀─────── │ web viewer   │
 │ (shared PC)  │ ◀─────── │  ID + relay      │ ───────▶ │ (browser)    │
 └──────────────┘  input   └──────────────────┘  frames  └──────────────┘
```

* **remoter-server** gives each host a 9-digit session ID, forwards a viewer's
  password attempt to the host, and relays traffic once the host accepts it.
  It also serves the web viewer. Repeated wrong passwords lock an ID for a
  minute.
* **remoter-host** runs on the computer being shared. It prints its ID and a
  random password, checks passwords itself (the server only passes them
  through), captures the screen while someone is watching, and receives mouse
  and keyboard events. It compares each capture with the last one in 64×64
  tiles and sends only the tiles that changed, as JPEG. Tiles that stop
  changing are resent once at higher quality so text ends up sharp. Viewers
  acknowledge each update, and the host never runs more than three updates
  ahead, so a slow link lowers the frame rate instead of adding lag.
* **web viewer** is a plain HTML/JS page: type the ID and password, see the
  screen, and your mouse and keyboard are forwarded.

The protocol is in [`internal/protocol`](internal/protocol/protocol.go).

## Try it

Needs Go 1.24+.

```sh
go run ./cmd/remoter-server            # listens on :8080
go run ./cmd/remoter-host              # shares display 0, prints ID + password
# or, on a machine without a display:
go run ./cmd/remoter-host -test-pattern
```

Open http://localhost:8080, enter the ID and password.

Host flags: `-server`, `-password`, `-fps`, `-quality`, `-display`,
`-view-only`, `-test-pattern`.

### Controlling a Windows computer

Run `remoter-server` and `remoter-host` on the Windows machine, then open
`http://<windows-ip>:8080` from the other computer. Click into the screen to
give it keyboard focus.

* Windows ignores injected input aimed at programs running as administrator
  (and at UAC prompts or the lock screen) unless the host also runs as
  administrator.
* Keys are sent by physical position, so the Windows keyboard layout decides
  which character is typed. Shortcuts the browser keeps for itself (such as
  Ctrl+W) cannot be forwarded.

## Development

```sh
go test -race ./...
```

## Layout

| Path | What |
| --- | --- |
| `cmd/remoter-server` | relay + web viewer binary |
| `cmd/remoter-host` | host agent binary |
| `internal/relay` | rendezvous, auth forwarding, frame/input relay |
| `internal/host` | host agent session logic |
| `internal/capture` | screen and test-pattern sources (Windows: reusable GDI capture) |
| `internal/stream` | changed-tile detection and the binary screen update format |
| `internal/input` | applies mouse/keyboard on the host (Windows: SendInput) |
| `internal/protocol` | wire messages |
| `web/static` | browser viewer |
