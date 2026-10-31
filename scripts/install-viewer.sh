#!/bin/sh
# Installs the Remoter viewer for the current user:
#   ~/.local/bin/remoter-viewer, plus an entry in the application menu.
# The viewer keeps itself up to date after that. Run it again to reinstall.
#
#   curl -fsSL https://github.com/aleynikovich/remoter/releases/download/<tag>/install-viewer.sh | sh
set -eu

repo=aleynikovich/remoter
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "No Remoter viewer build for $(uname -m)." >&2; exit 1 ;;
esac
asset=remoter-viewer-linux-$arch

bin=${XDG_BIN_HOME:-$HOME/.local/bin}
data=${XDG_DATA_HOME:-$HOME/.local/share}
mkdir -p "$bin" "$data/applications" "$data/icons/hicolor/scalable/apps"

# GitHub's release order is not by date: take the highest viewer-dev-N
# that has this build (what the viewer's own updater does too).
url=$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=100" |
	grep -o "\"browser_download_url\": *\"[^\"]*/viewer-dev-[0-9]*/$asset\"" |
	sed 's/.*"\(https[^"]*\)"$/\1/; s/.*\/viewer-dev-\([0-9]*\)\/.*/\1 &/' |
	sort -n | tail -n 1 | cut -d" " -f2)
if [ -z "$url" ]; then
	echo "Could not find a Remoter viewer release." >&2
	exit 1
fi
echo "Downloading $url"
curl -fL --progress-bar -o "$bin/remoter-viewer.new" "$url"
chmod 755 "$bin/remoter-viewer.new"
mv -f "$bin/remoter-viewer.new" "$bin/remoter-viewer"

cat >"$data/icons/hicolor/scalable/apps/remoter-viewer.svg" <<'SVG'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect x="4" y="8" width="56" height="38" rx="5" fill="#3b7bf6"/>
  <rect x="9" y="13" width="46" height="28" rx="2" fill="#1b1d23"/>
  <path d="M22 54h20M32 46v8" stroke="#3b7bf6" stroke-width="5" stroke-linecap="round"/>
  <path d="M27 21l12 6-12 6z" fill="#e8eaee"/>
</svg>
SVG

cat >"$data/applications/remoter-viewer.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Remoter
Comment=Control a remote computer
Exec=$bin/remoter-viewer
Icon=remoter-viewer
Terminal=false
Categories=Network;RemoteAccess;
StartupWMClass=remoter-viewer
DESKTOP
command -v update-desktop-database >/dev/null && update-desktop-database "$data/applications" >/dev/null 2>&1 || true

echo "Installed $("$bin/remoter-viewer" -version) to $bin/remoter-viewer"

# SDL 2 draws the window and FFmpeg decodes the video; both come from the
# system. Say what to install if either is missing.
has() { ldconfig -p 2>/dev/null | grep -q "$1" || ls /usr/lib*/$1* /usr/lib/*/$1* >/dev/null 2>&1; }
missing=""
has libSDL2-2.0.so.0 || missing="$missing sdl"
has libavcodec.so. || missing="$missing ffmpeg"
if [ -n "$missing" ]; then
	if command -v pacman >/dev/null; then pkgs="sdl2 ffmpeg"; cmd="sudo pacman -S --needed"
	elif command -v apt-get >/dev/null; then pkgs="libsdl2-2.0-0 libavcodec-extra"; cmd="sudo apt install"
	elif command -v dnf >/dev/null; then pkgs="SDL2 ffmpeg-free"; cmd="sudo dnf install"
	elif command -v zypper >/dev/null; then pkgs="libSDL2-2_0-0 ffmpeg"; cmd="sudo zypper install"
	else pkgs="SDL2 and FFmpeg"; cmd="install"
	fi
	echo
	echo "The viewer needs SDL 2 and FFmpeg's libavcodec. Install them with:"
	echo "  $cmd $pkgs"
fi
case ":$PATH:" in
*":$bin:"*) ;;
*) echo; echo "Add $bin to your PATH to start it as 'remoter-viewer', or use the Remoter menu entry." ;;
esac
