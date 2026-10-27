# Remoter

An open-source remote desktop tool in the spirit of TeamViewer: share your
screen, let someone control it, and connect with a 9-digit ID and a password.

> Status: early prototype. Screen streaming works end to end; remote control
> input is sent to the host but not applied yet. See the milestone issues.

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
  through), captures the screen as JPEG frames while someone is watching, and
  receives mouse and keyboard events.
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

Host flags: `-server`, `-password`, `-fps`, `-quality`, `-display`, `-test-pattern`.

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
| `internal/capture` | screen and test-pattern sources |
| `internal/protocol` | wire messages |
| `web/static` | browser viewer |
