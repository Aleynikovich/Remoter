# Remoter for Android

A native viewer for phones, tablets and foldables (tuned for the unfolded
Galaxy Z Fold). It speaks the same relay protocol as the browser viewer:
H.264 frames decoded in hardware with MediaCodec, JPEG tiles as a fallback,
mouse and keyboard input and two-way clipboard.

**Install:** open
https://github.com/aleynikovich/remoter/releases/download/android-latest/remoter-android.apk
on the phone and allow your browser or Files app to install it. Later builds
show up as an update banner on the app's start screen.

## Using it

- Start screen: saved computers with a live "online" dot (GET `/version`),
  then the other devices on the tailnet. Android apps cannot read
  Tailscale's device list, so the app asks each host that answers for its
  `GET /devices`. Tap to connect; the password defaults to `root` and is
  asked for only if the host refuses it. Long-press for password or remove.
- **Touch mouse** (default, like TeamViewer): slide to move the pointer, tap
  to click, long-press (or tap, then touch again) to drag; a zoomed picture
  scrolls to follow the pointer. **Direct touch**: tap clicks where you
  touch, drag drags, long-press is a right click.
- Both modes: pinch to zoom, two-finger drag scrolls, two-finger tap is a
  right click, three fingers pan the zoomed picture, three-finger tap opens
  the keyboard.
- Keys bar: Esc, Tab, arrows, Home/End/PgUp/PgDn, Del, F1–F12, PrtSc and
  latching Ctrl/Alt/Shift/Win (tap: next key or click; tap twice: the key
  alone; hold: lock).
- Mouse and keyboard (DeX, Bluetooth, USB): keys go by physical position;
  **Lock mouse** captures the pointer, Ctrl+Alt+Esc lets go.
- Folding, unfolding, rotating and DeX keep the session; the picture refits.

## Building

    cd android && ./gradlew assembleRelease

Every build is signed with `app/remoter-debug.keystore` (password
`android`), a sideloading key kept in the repo so each build installs over
the last. CI (`.github/workflows/android.yml`) publishes pushes that touch
`android/` to the rolling `android-latest` prerelease. It never carries host
binaries, so Windows hosts never pick it up as an update.
