package input

// Windows virtual-key codes for KeyboardEvent.code values. The code names the
// physical key, so the host's own keyboard layout decides which character it
// types, just like a local keyboard would.
var vkByCode = map[string]uint16{
	"Backspace": 0x08, "Tab": 0x09, "Enter": 0x0D, "NumpadEnter": 0x0D,
	"ShiftLeft": 0xA0, "ShiftRight": 0xA1, "ControlLeft": 0xA2, "ControlRight": 0xA3,
	"AltLeft": 0xA4, "AltRight": 0xA5, "Pause": 0x13, "CapsLock": 0x14,
	"Escape": 0x1B, "Space": 0x20, "PageUp": 0x21, "PageDown": 0x22,
	"End": 0x23, "Home": 0x24, "ArrowLeft": 0x25, "ArrowUp": 0x26,
	"ArrowRight": 0x27, "ArrowDown": 0x28, "PrintScreen": 0x2C, "Insert": 0x2D,
	"Delete": 0x2E, "MetaLeft": 0x5B, "MetaRight": 0x5C, "ContextMenu": 0x5D,
	"NumpadMultiply": 0x6A, "NumpadAdd": 0x6B, "NumpadSubtract": 0x6D,
	"NumpadDecimal": 0x6E, "NumpadDivide": 0x6F, "NumLock": 0x90, "ScrollLock": 0x91,
	"Semicolon": 0xBA, "Equal": 0xBB, "Comma": 0xBC, "Minus": 0xBD, "Period": 0xBE,
	"Slash": 0xBF, "Backquote": 0xC0, "BracketLeft": 0xDB, "Backslash": 0xDC,
	"BracketRight": 0xDD, "Quote": 0xDE, "IntlBackslash": 0xE2,
}

// extendedCodes need KEYEVENTF_EXTENDEDKEY, otherwise Windows treats e.g. the
// arrow keys as their numpad twins.
var extendedCodes = map[string]bool{
	"ControlRight": true, "AltRight": true, "NumpadEnter": true, "NumpadDivide": true,
	"Insert": true, "Delete": true, "Home": true, "End": true, "PageUp": true,
	"PageDown": true, "ArrowLeft": true, "ArrowUp": true, "ArrowRight": true,
	"ArrowDown": true, "MetaLeft": true, "MetaRight": true, "ContextMenu": true,
	"PrintScreen": true, "NumLock": true,
}

func init() {
	for c := 'A'; c <= 'Z'; c++ {
		vkByCode["Key"+string(c)] = uint16(c)
	}
	for d := '0'; d <= '9'; d++ {
		vkByCode["Digit"+string(d)] = uint16(d)
		vkByCode["Numpad"+string(d)] = 0x60 + uint16(d-'0')
	}
	for i := 1; i <= 24; i++ {
		vkByCode["F"+itoa(i)] = 0x70 + uint16(i-1)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// virtualKey returns the virtual-key code for a KeyboardEvent.code and
// whether it is an extended key. ok is false for codes without a mapping.
func virtualKey(code string) (vk uint16, extended, ok bool) {
	vk, ok = vkByCode[code]
	return vk, extendedCodes[code], ok
}
