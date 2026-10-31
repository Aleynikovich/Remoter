//go:build linux

package input

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// xkb tracks modifier state for the virtual keyboard the way the compositor
// would for a real one: the virtual keyboard protocol leaves that to the
// client. It uses libxkbcommon when it loads and a fixed table otherwise.
type xkb struct {
	keymap string
	state  uintptr // xkb_state*, 0 without libxkbcommon
	held   map[uint32]bool
	lock   uint32 // Caps and Num Lock without libxkbcommon
}

var xkbLib struct {
	contextNew       func(flags int32) uintptr
	keymapFromString func(ctx uintptr, s string, format, flags int32) uintptr
	keymapAsString   func(km uintptr, format int32) *byte
	stateNew         func(km uintptr) uintptr
	updateKey        func(st uintptr, key uint32, dir int32) int32
	serializeMods    func(st uintptr, which int32) uint32
	serializeLayout  func(st uintptr, which int32) uint32
	free             func(p *byte)
}

func loadXKB() bool {
	h, err := purego.Dlopen("libxkbcommon.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return false
	}
	libc, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return false
	}
	ok := true
	reg := func(fn any, lib uintptr, name string) {
		sym, err := purego.Dlsym(lib, name)
		if err != nil {
			ok = false
			return
		}
		purego.RegisterFunc(fn, sym)
	}
	reg(&xkbLib.contextNew, h, "xkb_context_new")
	reg(&xkbLib.keymapFromString, h, "xkb_keymap_new_from_string")
	reg(&xkbLib.keymapAsString, h, "xkb_keymap_get_as_string")
	reg(&xkbLib.stateNew, h, "xkb_state_new")
	reg(&xkbLib.updateKey, h, "xkb_state_update_key")
	reg(&xkbLib.serializeMods, h, "xkb_state_serialize_mods")
	reg(&xkbLib.serializeLayout, h, "xkb_state_serialize_layout")
	reg(&xkbLib.free, libc, "free")
	return ok
}

// newXKB builds the keymap for the host's keyboard layout.
func newXKB() *xkb {
	layout := hostLayout()
	k := &xkb{held: make(map[uint32]bool), keymap: fmt.Sprintf(`xkb_keymap {
	xkb_keycodes { include "evdev+aliases(qwerty)" };
	xkb_types { include "complete" };
	xkb_compat { include "complete" };
	xkb_symbols { include "pc+%s+inet(evdev)" };
};
`, layout)}
	if !loadXKB() {
		return k
	}
	ctx := xkbLib.contextNew(0)
	if ctx == 0 {
		return k
	}
	km := xkbLib.keymapFromString(ctx, k.keymap, 1, 0)
	if km == 0 && layout != "us" {
		k.keymap = strings.Replace(k.keymap, "+"+layout+"+", "+us+", 1)
		km = xkbLib.keymapFromString(ctx, k.keymap, 1, 0)
	}
	if km == 0 {
		return k
	}
	// Send the compiled keymap, so the compositor need not resolve includes.
	if p := xkbLib.keymapAsString(km, 1); p != nil {
		k.keymap = cString(p)
		xkbLib.free(p)
	}
	k.state = xkbLib.stateNew(km)
	return k
}

// key updates the state for a key and returns the serialised modifiers.
func (k *xkb) key(code uint32, down bool) (depressed, latched, locked, group uint32) {
	if k.state != 0 {
		dir := int32(0)
		if down {
			dir = 1
		}
		xkbLib.updateKey(k.state, code+8, dir)
		return xkbLib.serializeMods(k.state, 1), xkbLib.serializeMods(k.state, 2),
			xkbLib.serializeMods(k.state, 4), xkbLib.serializeLayout(k.state, 0x80)
	}
	if down && !k.held[code] {
		switch code {
		case 58: // Caps Lock
			k.lock ^= 2
		case 69: // Num Lock
			k.lock ^= 16
		}
	}
	if down {
		k.held[code] = true
	} else {
		delete(k.held, code)
	}
	for c := range k.held {
		switch c {
		case 42, 54:
			depressed |= 1 // Shift
		case 29, 97:
			depressed |= 4 // Control
		case 56:
			depressed |= 8 // Alt
		case 100:
			depressed |= 128 // AltGr (level 3)
		case 125, 126:
			depressed |= 64 // Super
		}
	}
	return depressed, 0, k.lock, 0
}

var layoutName = regexp.MustCompile(`^[a-z]{2,12}(\([a-z0-9_]{1,24}\))?$`)

// hostLayout is the first keyboard layout configured on the host, in XKB
// symbols form ("us", "de(nodeadkeys)"): $REMOTER_XKB_LAYOUT, Hyprland's
// input settings, $XKB_DEFAULT_LAYOUT, else us.
func hostLayout() string {
	if l := os.Getenv("REMOTER_XKB_LAYOUT"); layoutName.MatchString(l) {
		return l
	}
	first := func(s string) string { s, _, _ = strings.Cut(strings.TrimSpace(s), ","); return strings.TrimSpace(s) }
	layout, variant := first(os.Getenv("XKB_DEFAULT_LAYOUT")), first(os.Getenv("XKB_DEFAULT_VARIANT"))
	if l := hyprOption("input:kb_layout"); l != "" {
		layout, variant = first(l), first(hyprOption("input:kb_variant"))
	}
	if variant != "" {
		layout += "(" + variant + ")"
	}
	if layoutName.MatchString(layout) {
		return layout
	}
	return "us"
}

func hyprOption(name string) string {
	if _, err := exec.LookPath("hyprctl"); err != nil {
		return ""
	}
	cmd := exec.Command("hyprctl", "-j", "getoption", name)
	if os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") == "" {
		// A background service is started without Hyprland's environment;
		// find the running instance the way hyprctl itself would.
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			dir = fmt.Sprintf("/run/user/%d", os.Getuid())
		}
		sigs, _ := filepath.Glob(filepath.Join(dir, "hypr", "*", ".socket.sock"))
		if len(sigs) == 0 {
			return ""
		}
		cmd.Env = append(os.Environ(), "HYPRLAND_INSTANCE_SIGNATURE="+filepath.Base(filepath.Dir(sigs[len(sigs)-1])))
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var opt struct {
		Str string `json:"str"`
	}
	if json.Unmarshal(out, &opt) != nil || opt.Str == "[[EMPTY]]" {
		return ""
	}
	return opt.Str
}

func cString(p *byte) string {
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
