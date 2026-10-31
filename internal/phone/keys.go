package phone

// Android key codes the viewer sends for buttons and shortcuts.
const (
	KeyHome       = 3
	KeyBack       = 4
	KeyVolumeUp   = 24
	KeyVolumeDown = 25
	KeyPower      = 26
	KeyAppSwitch  = 187
)

// Keycode maps a USB HID usage (which SDL scancodes are) to an Android key
// code. typed reports whether the key types a character, which the viewer
// sends as text instead unless Ctrl, Alt or the Windows key is held.
func Keycode(scancode int) (keycode int, typed bool) {
	switch {
	case scancode >= 4 && scancode <= 29: // A..Z
		return 29 + scancode - 4, true
	case scancode >= 30 && scancode <= 38: // 1..9
		return 8 + scancode - 30, true
	case scancode == 39: // 0
		return 7, true
	case scancode >= 58 && scancode <= 69: // F1..F12
		return 131 + scancode - 58, false
	}
	if k, ok := typedKeys[scancode]; ok {
		return k, true
	}
	if k, ok := otherKeys[scancode]; ok {
		return k, false
	}
	return 0, false
}

var typedKeys = map[int]int{
	44: 62, // space
	45: 69, // minus
	46: 70, // equals
	47: 71, // [
	48: 72, // ]
	49: 73, // backslash
	51: 74, // ;
	52: 75, // '
	53: 68, // `
	54: 55, // ,
	55: 56, // .
	56: 76, // /
}

var otherKeys = map[int]int{
	40:  66,  // Enter
	88:  66,  // keypad Enter
	41:  111, // Escape
	42:  67,  // Backspace (DEL)
	43:  61,  // Tab
	73:  124, // Insert
	74:  122, // Home (MOVE_HOME)
	75:  92,  // Page Up
	76:  112, // Delete (FORWARD_DEL)
	77:  123, // End (MOVE_END)
	78:  93,  // Page Down
	79:  22,  // Right
	80:  21,  // Left
	81:  20,  // Down
	82:  19,  // Up
	101: 82,  // Menu
}
