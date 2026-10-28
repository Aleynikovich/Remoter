package viewer

// CodeForScancode returns the KeyboardEvent.code name the host expects for a
// USB HID keyboard usage (which is what SDL calls a scancode), or "" for keys
// the host does not know. Like the browser viewer, the viewer sends physical
// keys, so the host's keyboard layout decides what they type.
func CodeForScancode(sc int) string {
	if sc >= 0 && sc < len(codes) {
		return codes[sc]
	}
	switch sc {
	case 224:
		return "ControlLeft"
	case 225:
		return "ShiftLeft"
	case 226:
		return "AltLeft"
	case 227:
		return "MetaLeft"
	case 228:
		return "ControlRight"
	case 229:
		return "ShiftRight"
	case 230:
		return "AltRight"
	case 231:
		return "MetaRight"
	}
	return ""
}

// codes is indexed by HID usage, keyboard page.
var codes = func() []string {
	c := make([]string, 116)
	for i := range 26 {
		c[4+i] = "Key" + string(rune('A'+i))
	}
	for i := range 9 {
		c[30+i] = "Digit" + string(rune('1'+i))
		c[89+i] = "Numpad" + string(rune('1'+i))
	}
	c[39], c[98] = "Digit0", "Numpad0"
	for i := range 12 {
		c[58+i] = "F" + itoa(i+1)
		c[104+i] = "F" + itoa(i+13)
	}
	named := map[int]string{
		40: "Enter", 41: "Escape", 42: "Backspace", 43: "Tab", 44: "Space",
		45: "Minus", 46: "Equal", 47: "BracketLeft", 48: "BracketRight",
		49: "Backslash", 50: "Backslash", 51: "Semicolon", 52: "Quote",
		53: "Backquote", 54: "Comma", 55: "Period", 56: "Slash", 57: "CapsLock",
		70: "PrintScreen", 71: "ScrollLock", 72: "Pause", 73: "Insert",
		74: "Home", 75: "PageUp", 76: "Delete", 77: "End", 78: "PageDown",
		79: "ArrowRight", 80: "ArrowLeft", 81: "ArrowDown", 82: "ArrowUp",
		83: "NumLock", 84: "NumpadDivide", 85: "NumpadMultiply",
		86: "NumpadSubtract", 87: "NumpadAdd", 88: "NumpadEnter",
		99: "NumpadDecimal", 100: "IntlBackslash", 101: "ContextMenu",
	}
	for sc, name := range named {
		c[sc] = name
	}
	return c
}()

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
