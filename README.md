# Remoter

An open-source remote desktop tool in the spirit of TeamViewer: share your
screen, let someone control it, and connect with a 9-digit ID and a password.

> Status: early prototype. Screen streaming works everywhere; mouse and
> keyboard control works on Windows and Linux (macOS is still view-only).
> See the milestone issues.

## How it works

```
 ┌──────────────┐   wss    ┌──────────────────┐   wss    ┌──────────────┐
 │ remoter host │ ───────▶ │  remoter relay   │ ◀─────── │ web viewer   │
 │ (shared PC)  │ ◀─────── │  ID + relay      │ ───────▶ │ (browser)    │
 └──────────────┘  input   └──────────────────┘  frames  └──────────────┘
```

* **the relay** gives each host a 9-digit session ID, forwards a viewer's
  password attempt to the host, and relays traffic once the host accepts it.
  It also serves the web viewer. Repeated wrong passwords lock an ID for a
  minute.
* **the host** runs on the computer being shared. It prints its ID and a
  password (`root` by default), checks passwords itself (the server only passes them
  through), captures the screen while someone is watching, and receives mouse
  and keyboard events. It compares each capture with the last one in 64×64
  tiles and sends only the tiles that changed, as JPEG. Tiles that stop
  changing are resent once at higher quality so text ends up sharp. Viewers
  acknowledge each update, and the host never runs more than three updates
  ahead, so a slow link lowers the frame rate instead of adding lag.
* **web viewer** is a plain HTML/JS page: type the ID and password, see the
  screen, and your mouse and keyboard are forwarded.

### Video

When the viewer's browser can decode H.264 (WebCodecs: Chrome, Edge,
Vivaldi, Brave, Safari, recent Firefox), the host sends H.264 video instead
of JPEG tiles. That is smoother and needs far less bandwidth: a window drag
is a few Mbit/s instead of tens. Video is encoded with Cisco's OpenH264
library, which Remoter downloads from Cisco on first use on Windows (about
1 MB, kept in `%LocalAppData%\Remoter`; Cisco's H.264 patent license covers
copies downloaded from them). On Linux install `libopenh264-7`. The codec is
tried in a child process before it is used, and anything that goes wrong
falls back to JPEG tiles.

Browsers only offer WebCodecs on HTTPS or localhost. Over plain
`http://<tailscale-ip>:8080`, add the address to
`chrome://flags/#unsafely-treat-insecure-origin-as-secure` (see below), or the
viewer stays on JPEG. `-bitrate` sets the target in kbit/s (default 8000),
`-no-video` turns video off.

The protocol is in [`internal/protocol`](internal/protocol/protocol.go).

## Try it

Needs Go 1.24+.

```sh
go run ./cmd/remoter                   # relay on :8080 + shares display 0
# or, on a machine without a display:
go run ./cmd/remoter -test-pattern
```

One program runs both the relay (with the web viewer) and the host. To run
them on different machines use `remoter -mode server` on the relay and
`remoter -mode host -server ws://<relay>:8080/ws/host` on the shared
computer.

Open http://localhost:8080, enter the ID and password.

The host's password is `root` unless you pass `-password` (use
`-password ""` for a random one). When only one computer is shared on the
server, the viewer needs just the password; the partner ID is only asked for
when several are connected.

Flags: `-mode`, `-addr`, `-server`, `-password`, `-fps`, `-quality`,
`-display`, `-view-only`, `-no-clipboard`, `-test-pattern`, `-no-update`,
`-update-interval`, `-version`, `-console`, `-stop`, `-no-autostart`.

### Running in the background (Windows)

On Windows the window you start Remoter from only launches it: Remoter
itself runs in the background with no window, so closing the window never
drops the connection, and it adds itself to the programs Windows starts when
you log in. The window says where it logs (`%LOCALAPPDATA%\Remoter\remoter.log`).
Starting it again while it runs does nothing if it is the same build, and
replaces the running copy if it is a different one. If an older Remoter
still holds the port, the new one waits and takes over once that is closed.
`remoter -stop` stops it (`-stop -no-autostart` also stops starting it at
login), and `-console` runs it in the window as before.

### Updates

Release builds (from the [releases page](https://github.com/aleynikovich/remoter/releases))
update themselves. Every two minutes they look for a newer release that has
a `remoter-<os>-<arch>` file, download it next to the running program, check
its size and SHA-256 and that it starts, swap it in (the previous version is
kept as `remoter.old.exe` until the next start), and restart in the same
window. The host keeps its ID across the restart and open viewers reconnect
on their own. Every push to `main` or a work branch publishes a dev release,
so updates arrive within minutes of a change. `-no-update` turns this off;
builds made with plain `go build` never update.

### Controlling a Windows computer

Download `remoter-windows-amd64.exe` from the latest release and run it on
the Windows machine, then open
`http://<windows-ip>:8080` from the other computer. Click into the screen to
give it keyboard focus.

* Windows ignores injected input aimed at programs running as administrator
  (and at UAC prompts or the lock screen) unless the host also runs as
  administrator.
* The clipboard is shared both ways (text only). Ctrl+V in the viewer pastes
  your local clipboard on the remote computer. Anything copied on the remote
  computer lands in your local clipboard; browsers only allow that right
  after a click or key press, so if it arrives later it is copied on your next
  click or key press in the viewer. `-no-clipboard` turns sharing off.
* On a Mac viewer, Cmd is sent as Ctrl, so Cmd+C and Cmd+V work as expected.
* The pointer in the viewer takes the remote pointer's shape (text beam,
  hand, resize arrows...), so it reacts instantly instead of waiting for the
  screen image.
* The toolbar has a display picker (when the host has several monitors), a
  1:1 / Fit toggle, full screen, and a "Send keys" menu for combinations your
  own computer would catch first (Windows key, Alt+Tab, Ctrl+Shift+Esc...).
  Ctrl+Alt+Del cannot be sent: Windows only accepts it from a system service.
* In full screen, Chromium browsers (Chrome, Edge, Vivaldi, Brave) lock the
  keyboard, so browser shortcuts, Esc, Alt+Tab and the Windows key go to the
  remote computer (hold Esc to leave). Keyboard Lock only works on HTTPS or
  localhost. On plain `http://` over a private network, tell the browser to
  trust the address: open `chrome://flags/#unsafely-treat-insecure-origin-as-secure`
  (`vivaldi://flags/...` in Vivaldi, `edge://flags/...` in Edge), add e.g.
  `http://100.102.208.35:8080`, enable it and restart the browser. That also
  lets the clipboard work without waiting for a click.

### Desktop viewer (Linux and Windows)

`remoter-viewer` is a native window instead of a browser tab: H.264 drawn by
the GPU without a browser in between, and the keyboard and mouse fully
handed to the remote computer. It opens on a list of the computers on your
Tailscale network, those running Remoter first; pick one and you are
connected (with the hosts' default password, asking only if a computer uses
another). "Connect by address…" is there for anything else.

* **Linux**: install it for your user (it lands in `~/.local/bin` with a
  menu entry, and updates itself):

  ```sh
  curl -fsSL https://raw.githubusercontent.com/aleynikovich/remoter/main/scripts/install-viewer.sh | sh
  ```

  It needs SDL 2 and FFmpeg from the system (Arch: `pacman -S sdl2 ffmpeg`,
  Debian/Ubuntu: `apt install libsdl2-2.0-0 libavcodec-extra`).
* **Windows**: download `remoter-viewer-windows-amd64.exe` from the newest
  viewer release and run it; it updates itself. On first start it fetches
  SDL from the SDL project (checksum pinned) and OpenH264 from Cisco, like the
  host, into `%LocalAppData%\Remoter`, where it also writes `viewer.log`.

`remoter-viewer 100.102.208.35:8080` skips the list and connects straight
away.

* While connected, every key goes to the remote computer, including your
  desktop's own shortcuts (Super+Q, Alt+Tab, workspace binds): on Wayland
  the viewer uses the keyboard-shortcuts-inhibit protocol, which Hyprland,
  Sway, GNOME and KDE support; on Windows a low-level keyboard hook takes
  the Windows key, Alt+Tab and the like (only Ctrl+Alt+Del stays local).
  The pointer is kept inside the window.
  **Ctrl+Alt+D** gives keyboard and mouse back; click the picture to hand
  them over again. In Hyprland, binds marked `bindp` still stay local.
* **Ctrl+Alt+Enter** toggles full screen, **Ctrl+Alt+PageDown/PageUp**
  switches between the remote computer's monitors, and **Ctrl+Alt+I** copies
  a diagnostics report (a ping and download test, the viewer's frame,
  decode and draw times for the last minute, then the host's settings and
  log) to the clipboard.
* Point at the blue tab in the middle of the top edge (or press
  **Ctrl+Alt+M**) for the toolbar: picture quality, monitor, full screen,
  Send keys (Windows key, Alt+Tab, Ctrl+Shift+Esc, Win+L,
  Print Screen, Alt+F4), release or take control, send files, a link test,
  diagnostics, and back to the device list. Files dropped on the window, or
  picked with "Send files…", land in the remote computer's Downloads.
* **Ctrl+Alt+Q** steps the picture through Auto and 100% down to 40%;
  the title shows the setting and the scale video arrives at. Auto aims
  for 60 fps and moves the resolution between 40% and 100% to get there.
* The clipboard follows you both ways: copy on the remote computer and paste
  locally, or copy locally and press Ctrl+V in the remote one.
* `-jpeg` asks for JPEG tiles instead of video, `-fullscreen` starts full
  screen, `-no-update` stops updates.

### Mirroring an Android phone

The desktop viewer also shows an Android phone's screen and controls it with
the mouse and keyboard, without root and without installing anything on the
phone. It works like [scrcpy](https://github.com/Genymobile/scrcpy) (Apache
2.0), whose server it runs: the viewer pushes scrcpy's server to the phone
over ADB wireless debugging, which reaches the phone over Tailscale too. The
server streams the screen as H.264 and injects the taps and keys. The server
comes from scrcpy's GitHub release on first use, with its checksum pinned.

* Install ADB on the computer (Debian/Ubuntu: `sudo apt install adb`, Arch:
  `pacman -S android-tools`, Windows: `winget install Google.PlatformTools`).
* Phones appear in the viewer's Tailscale list; pick one and it opens in its
  own window (or run `remoter-viewer phone 100.95.169.40`).
* The first time, the window asks you to pair: on the phone, open
  Developer options › Wireless debugging and turn it on. Type the port shown
  under "IP address & Port", then tap "Pair device with pairing code" and
  type that dialog's port and code. The viewer then moves the phone's ADB to
  port 5555, so it reconnects by itself until the phone restarts. After a
  restart, turn Wireless debugging on again and type its port (no new code).
* Click to tap, drag to swipe, the wheel scrolls, right-click is Back and
  middle-click is Home. Typing goes to the phone in your keyboard's layout,
  and Ctrl+V pastes this computer's clipboard there. What you copy on the
  phone lands on this computer's clipboard.
* **Ctrl+Alt+S** saves a full-resolution screenshot to `Pictures/Remoter`
  and copies it to the clipboard (Linux needs `wl-clipboard` on Wayland or
  `xclip` on X11 for the copy). `remoter-viewer phone-shot` does the same
  without the window, for a desktop hotkey, e.g. in `hyprland.conf`:
  `bind = SUPER SHIFT, P, exec, remoter-viewer phone-shot`.
* Ctrl+Alt+H Home, B Back, R recent apps, N notifications, P power, O turns
  the phone's own screen off (or back on) while mirroring goes on, Up/Down
  volume, Enter full screen.
* Apps that block screen capture (banking apps such as Revolut, or anything
  that sets `FLAG_SECURE`) show black, in the mirror and in screenshots.
  Android does that for every capture method on a phone without root.

### Sharing a Linux PC

`remoter` shares a Linux desktop too, with keyboard, mouse and clipboard:

* **Hyprland, Sway and other wlroots compositors**: the screen is captured
  with wlr-screencopy and input goes through the virtual keyboard and pointer
  protocols, so it needs no root and no extra setup. The virtual keyboard
  uses your first layout from Hyprland's `input:kb_layout` (or
  `$REMOTER_XKB_LAYOUT`, e.g. `de(nodeadkeys)`).
* **X11 sessions** (GNOME or KDE on Xorg, XFCE...): capture through X11 and
  input through XTEST.
* GNOME and KDE on Wayland are not supported yet: they only allow capture
  through the desktop portal.

Install `openh264` (Arch) or `libopenh264-7` (Debian, Ubuntu) for H.264, and
`wl-clipboard` (Wayland) or `xclip` (X11) for the clipboard. Run as a
background service, Remoter finds the session's Wayland socket by itself.
`scripts/install-share.sh` sets that service up.

### Linux viewers with a tiling compositor (Hyprland, Sway...)

The desktop viewer above handles this by itself. In a browser, the
compositor sees key combinations before the page does, so binds such
as Super+Q never reach the remote computer, even with Keyboard Lock. Add a
passthrough mode that disables your binds while you work on the remote
machine. For Hyprland, in `hyprland.conf`:

```
# Super+Escape toggles passthrough: all keys go to the focused window.
bind = SUPER, Escape, submap, passthru
submap = passthru
bind = SUPER, Escape, submap, reset
submap = reset
```

Press Super+Escape with the viewer in full screen, work on the remote
computer, and press Super+Escape again to get your binds back. Super+Escape
itself and the kernel's Ctrl+Alt+F1..F12 VT switch still stay local. In Sway
the same idea is a `mode "passthrough"` with only a binding to leave it.
* Keys still held when the viewer loses focus are released on the remote
  computer, so Alt or Ctrl never stay stuck.
* Keys are sent by physical position, so the Windows keyboard layout decides
  which character is typed. Shortcuts the browser keeps for itself (such as
  Ctrl+W) cannot be forwarded.

## Sending files between your devices

Like KDE Connect, but over Tailscale and built into Remoter: share a PDF or
photo from the phone to a PC, send one from the PC to the phone, or send a
link or the clipboard either way. Only devices on your tailnet can send.

- **Windows:** the Remoter you already run receives. Files go to Downloads
  with a notification; text and links go to the clipboard.
- **Linux:** install Remoter as a systemd user service (it also shares the
  desktop, see below; `REMOTER_ARGS="-mode share"` for receiving only):

      curl -fsSL https://raw.githubusercontent.com/aleynikovich/remoter/main/scripts/install-share.sh | sh

  Then `remoter send file.pdf`, `remoter send -text https://…`,
  `remoter send -clipboard`, or `remoter send` alone for a file chooser
  (bind it to a key; the script prints Hyprland lines). With several
  devices a launcher menu (fuzzel, wofi, rofi…) asks which; phones come
  first. http://localhost:8080/share/ takes dropped or pasted files.
- **Android:** share anything to **Remoter** and pick the PC. The app
  receives in the background (a quiet notification says so; switch it off
  on the start screen) into Downloads.

The API is plain HTTP on port 8080 (`internal/share`): `GET /share/info`,
`POST /share/file?name=…`, `POST /share/text`, with the sender in an
`X-Remoter-From` header.

## Development

```sh
go test -race ./...
```

## Layout

| Path | What |
| --- | --- |
| `cmd/remoter` | the binary: relay + web viewer and/or host agent |
| `internal/update` | self-update from GitHub releases |
| `internal/relay` | rendezvous, auth forwarding, frame/input relay |
| `internal/host` | host agent session logic |
| `internal/capture` | screen and test-pattern sources (Windows: reusable GDI capture) |
| `internal/stream` | changed-tile detection and the binary screen update format |
| `internal/clipboard` | host clipboard access (Windows) |
| `internal/cursor` | host pointer shape (Windows) |
| `internal/input` | applies mouse/keyboard on the host (Windows: SendInput) |
| `internal/protocol` | wire messages |
| `web/static` | browser viewer |
| `cmd/remoter-viewer` | desktop viewer for Linux and Windows (SDL 2 and FFmpeg or OpenH264 loaded at run time) |
| `internal/viewer` | desktop viewer connection, Tailscale device list, key map and settings |
| `internal/phone` | Android phone mirroring: ADB, scrcpy server protocol, screenshots |
