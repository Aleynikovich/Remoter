#!/bin/sh
# Installs Remoter file sharing for the current user on Linux, so your phone
# and PCs can send each other files, photos, PDFs and links:
#   ~/.local/bin/remoter                    the program (keeps itself up to date)
#   remoter-share.service (systemd --user)  receives into ~/Downloads with a notification
#   "Send to phone" in the application menu and the file manager's Open With
# Run it again to reinstall.
#
#   curl -fsSL https://raw.githubusercontent.com/aleynikovich/remoter/main/scripts/install-share.sh | sh
set -eu

repo=aleynikovich/remoter
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
*) echo "No Remoter build for $(uname -m) yet." >&2; exit 1 ;;
esac
asset=remoter-linux-$arch

bin=${XDG_BIN_HOME:-$HOME/.local/bin}
data=${XDG_DATA_HOME:-$HOME/.local/share}
units=${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user
mkdir -p "$bin" "$data/applications" "$units"

# GitHub sorts releases by tag name, not date: pick the highest dev-N that
# has the Linux build (what the built-in updater does too).
url=$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=100" | python3 -c '
import json, re, sys
best = (-1, "")
for r in json.load(sys.stdin):
    m = re.fullmatch(r"dev-(\d+)", r["tag_name"])
    for a in r["assets"]:
        if m and a["name"] == sys.argv[1] and int(m[1]) > best[0]:
            best = (int(m[1]), a["browser_download_url"])
print(best[1])
' "$asset")
if [ -z "$url" ]; then
	echo "Could not find a Remoter release for Linux." >&2
	exit 1
fi
echo "Downloading $url"
curl -fL --progress-bar -o "$bin/remoter.new" "$url"
chmod 755 "$bin/remoter.new"
mv -f "$bin/remoter.new" "$bin/remoter"

cat >"$units/remoter-share.service" <<UNIT
[Unit]
Description=Remoter: receive files and links from your phone and other PCs
After=network-online.target

[Service]
ExecStart=$bin/remoter -mode share
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
UNIT

cat >"$data/applications/remoter-send.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Send to phone
GenericName=Remoter Send
Comment=Send files to your phone or another PC with Remoter
Exec=$bin/remoter send %F
Icon=smartphone
Terminal=false
Categories=Network;FileTransfer;
MimeType=application/pdf;image/png;image/jpeg;image/gif;image/webp;image/heic;video/mp4;video/webm;text/plain;application/zip;application/octet-stream;all/allfiles;
DESKTOP
cat >"$data/applications/remoter-send-page.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=Remoter Send (drop files)
Comment=A window to drop or paste files to send to your phone
Exec=xdg-open http://localhost:8080/share/
Icon=smartphone
Terminal=false
Categories=Network;FileTransfer;
DESKTOP
command -v update-desktop-database >/dev/null && update-desktop-database "$data/applications" >/dev/null 2>&1 || true

# Files: right-click > Scripts > Send to phone.
if command -v nautilus >/dev/null; then
	mkdir -p "$data/nautilus/scripts"
	printf '#!/bin/sh\nexec "%s" send "$@"\n' "$bin/remoter" >"$data/nautilus/scripts/Send to phone"
	chmod 755 "$data/nautilus/scripts/Send to phone"
fi

# The service needs the desktop session's variables to open files and copy
# to the clipboard; take them from this session now.
systemctl --user import-environment WAYLAND_DISPLAY DISPLAY XDG_CURRENT_DESKTOP HYPRLAND_INSTANCE_SIGNATURE 2>/dev/null || true
systemctl --user daemon-reload
systemctl --user enable remoter-share.service >/dev/null 2>&1
systemctl --user restart remoter-share.service
echo "Installed $("$bin/remoter" -version) to $bin/remoter; receiving into $(xdg-user-dir DOWNLOAD 2>/dev/null || echo ~/Downloads)"

missing=""
command -v notify-send >/dev/null || missing="$missing libnotify-bin"
command -v wl-copy >/dev/null || missing="$missing wl-clipboard"
command -v zenity >/dev/null || command -v kdialog >/dev/null || missing="$missing zenity"
if [ -n "$missing" ]; then
	echo
	echo "For notifications, the clipboard and the file chooser, install:"
	echo "  sudo apt install$missing"
fi

cat <<HELP

Send from the terminal:   remoter send report.pdf photo.jpg
                          remoter send -text https://example.com
                          remoter send -clipboard
Hyprland key bindings (add to ~/.config/hypr/hyprland.conf):
  bind = SUPER, S, exec, $bin/remoter send                  # choose files, send to phone
  bind = SUPER SHIFT, S, exec, $bin/remoter send -clipboard # send the clipboard
  bind = SUPER CTRL, S, exec, grim -g "\$(slurp)" /tmp/remoter-shot.png && $bin/remoter send /tmp/remoter-shot.png
  # so files open from notifications after a reboot, if not already there:
  exec-once = dbus-update-activation-environment --systemd WAYLAND_DISPLAY XDG_CURRENT_DESKTOP
Drop or paste files at http://localhost:8080/share/ (menu: "Remoter Send (drop files)").
HELP
