// Package phone mirrors and controls an Android phone from the desktop
// viewer, the way scrcpy does: it pushes scrcpy's server to the phone over
// ADB (wireless debugging, so over Tailscale too), which streams the screen
// as H.264 and injects the touches and keys it is sent. Nothing is
// installed on the phone and no root is needed.
//
// The message layouts follow scrcpy 5.0.1 (app/src/control_msg.c,
// app/src/demuxer.c and app/src/device_msg.c), whose server this package
// runs; the server refuses clients of any other version.
package phone

import (
	"encoding/binary"
	"math"
)

// Control message types, in scrcpy's order.
const (
	msgInjectKeycode = iota
	msgInjectText
	msgInjectTouch
	msgInjectScroll
	msgBackOrScreenOn
	msgExpandNotifications
	msgExpandSettings
	msgCollapsePanels
	msgGetClipboard
	msgSetClipboard
	msgSetDisplayPower
	msgRotateDevice
	msgUHIDCreate
	msgUHIDInput
	msgUHIDDestroy
	msgOpenHardKeyboardSettings
	msgStartApp
	msgResetVideo
)

// Android KeyEvent and MotionEvent actions.
const (
	ActionDown = 0
	ActionUp   = 1
	ActionMove = 2
)

// MotionEvent.BUTTON_PRIMARY.
const buttonPrimary = 1

// pointerMouse is scrcpy's pointer ID for the computer's mouse. With only
// the primary button down the server injects it as a finger.
const pointerMouse = ^uint64(0)

// maxTextLen is the most text one inject-text message may carry, in bytes.
const maxTextLen = 300

// Android meta state bits for key events.
const (
	MetaShift = 0x01 | 0x40     // META_SHIFT_ON | META_SHIFT_LEFT_ON
	MetaAlt   = 0x02 | 0x10     // META_ALT_ON | META_ALT_LEFT_ON
	MetaCtrl  = 0x1000 | 0x2000 // META_CTRL_ON | META_CTRL_LEFT_ON
	MetaMeta  = 0x10000 | 0x20000
)

// position is a point on the phone screen with the video size it was
// measured against; the server drops events whose size is not the current
// one (the phone rotated in between).
func putPosition(b []byte, x, y, w, h int) {
	binary.BigEndian.PutUint32(b[0:], uint32(x))
	binary.BigEndian.PutUint32(b[4:], uint32(y))
	binary.BigEndian.PutUint16(b[8:], uint16(w))
	binary.BigEndian.PutUint16(b[10:], uint16(h))
}

// u16fp encodes 0..1 as scrcpy's unsigned 16-bit fixed point.
func u16fp(f float64) uint16 {
	f = min(max(f, 0), 1)
	if f == 1 {
		return 0xffff
	}
	return uint16(f * 0x10000)
}

// i16fp encodes -1..1 as scrcpy's signed 16-bit fixed point.
func i16fp(f float64) int16 {
	f = min(max(f, -1), 1)
	if f == 1 {
		return 0x7fff
	}
	return int16(math.Round(f * 0x8000))
}

// touchMsg is a pointer event at (x, y) of a w×h video.
func touchMsg(action int, x, y, w, h int) []byte {
	b := make([]byte, 32)
	b[0] = msgInjectTouch
	b[1] = byte(action)
	binary.BigEndian.PutUint64(b[2:], pointerMouse)
	putPosition(b[10:], x, y, w, h)
	var pressure float64
	var actionButton, buttons uint32
	switch action {
	case ActionDown:
		pressure, actionButton, buttons = 1, buttonPrimary, buttonPrimary
	case ActionUp:
		actionButton = buttonPrimary
	default:
		pressure, buttons = 1, buttonPrimary
	}
	binary.BigEndian.PutUint16(b[22:], u16fp(pressure))
	binary.BigEndian.PutUint32(b[24:], actionButton)
	binary.BigEndian.PutUint32(b[28:], buttons)
	return b
}

// scrollMsg scrolls at (x, y) by SDL wheel units (positive y is up),
// at most 16 per message.
func scrollMsg(x, y, w, h int, dx, dy float64) []byte {
	b := make([]byte, 21)
	b[0] = msgInjectScroll
	putPosition(b[1:], x, y, w, h)
	binary.BigEndian.PutUint16(b[13:], uint16(i16fp(dx/16)))
	binary.BigEndian.PutUint16(b[15:], uint16(i16fp(dy/16)))
	return b
}

func keycodeMsg(action, keycode, repeat, meta int) []byte {
	b := make([]byte, 14)
	b[0] = msgInjectKeycode
	b[1] = byte(action)
	binary.BigEndian.PutUint32(b[2:], uint32(keycode))
	binary.BigEndian.PutUint32(b[6:], uint32(repeat))
	binary.BigEndian.PutUint32(b[10:], uint32(meta))
	return b
}

// textMsgs types text, split into messages the server accepts without
// cutting a character in two.
func textMsgs(text string) [][]byte {
	var out [][]byte
	for text != "" {
		n := len(text)
		if n > maxTextLen {
			n = maxTextLen
			for n > 0 && text[n]&0xc0 == 0x80 { // inside a UTF-8 sequence
				n--
			}
		}
		b := make([]byte, 5+n)
		b[0] = msgInjectText
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		copy(b[5:], text[:n])
		out = append(out, b)
		text = text[n:]
	}
	return out
}

// backMsg presses BACK, or turns the screen on when it is off.
func backMsg(action int) []byte { return []byte{msgBackOrScreenOn, byte(action)} }

// setClipboardMsg puts text on the phone's clipboard and, with paste, pastes
// it where the phone's focus is.
func setClipboardMsg(seq uint64, text string, paste bool) []byte {
	b := make([]byte, 14+len(text))
	b[0] = msgSetClipboard
	binary.BigEndian.PutUint64(b[1:], seq)
	if paste {
		b[9] = 1
	}
	binary.BigEndian.PutUint32(b[10:], uint32(len(text)))
	copy(b[14:], text)
	return b
}

func displayPowerMsg(on bool) []byte {
	b := []byte{msgSetDisplayPower, 0}
	if on {
		b[1] = 1
	}
	return b
}
