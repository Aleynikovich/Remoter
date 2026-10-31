package io.github.aleynikovich.remoter

import android.view.KeyEvent

/**
 * Maps Android keys to the KeyboardEvent.code names the host expects. Like the
 * browser and desktop viewers, physical keys are sent by position, so the host's
 * own keyboard layout decides what they type.
 */
object Keys {
    /** Code for a key from a hardware keyboard, by its Linux scancode, or null. */
    fun codeForScancode(sc: Int): String? = scancodes[sc]

    /** Code for an Android key code (used for keys from the on-screen keyboard), or null. */
    fun codeForKeycode(kc: Int): String? = keycodes[kc]

    /** Code and whether Shift is needed to type [c] on a US layout, or null. */
    fun codeForChar(c: Char): Pair<String, Boolean>? {
        when (c) {
            in 'a'..'z' -> return "Key" + c.uppercaseChar() to false
            in 'A'..'Z' -> return "Key$c" to true
            in '0'..'9' -> return "Digit$c" to false
        }
        plain[c]?.let { return it to false }
        shifted[c]?.let { return it to true }
        return null
    }

    fun isModifier(code: String) = code in modifiers

    private val modifiers = setOf(
        "ControlLeft", "ControlRight", "ShiftLeft", "ShiftRight",
        "AltLeft", "AltRight", "MetaLeft", "MetaRight",
    )

    private val plain = mapOf(
        ' ' to "Space", '-' to "Minus", '=' to "Equal", '[' to "BracketLeft",
        ']' to "BracketRight", '\\' to "Backslash", ';' to "Semicolon", '\'' to "Quote",
        ',' to "Comma", '.' to "Period", '/' to "Slash", '`' to "Backquote",
        '\n' to "Enter", '\t' to "Tab",
    )

    private val shifted = mapOf(
        '!' to "Digit1", '@' to "Digit2", '#' to "Digit3", '$' to "Digit4", '%' to "Digit5",
        '^' to "Digit6", '&' to "Digit7", '*' to "Digit8", '(' to "Digit9", ')' to "Digit0",
        '_' to "Minus", '+' to "Equal", '{' to "BracketLeft", '}' to "BracketRight",
        '|' to "Backslash", ':' to "Semicolon", '"' to "Quote", '<' to "Comma",
        '>' to "Period", '?' to "Slash", '~' to "Backquote",
    )

    // Linux input event codes (include/uapi/linux/input-event-codes.h).
    private val scancodes: Map<Int, String> = buildMap {
        put(1, "Escape")
        "1234567890".forEachIndexed { i, c -> put(2 + i, "Digit$c") }
        put(12, "Minus"); put(13, "Equal"); put(14, "Backspace"); put(15, "Tab")
        "QWERTYUIOP".forEachIndexed { i, c -> put(16 + i, "Key$c") }
        put(26, "BracketLeft"); put(27, "BracketRight"); put(28, "Enter"); put(29, "ControlLeft")
        "ASDFGHJKL".forEachIndexed { i, c -> put(30 + i, "Key$c") }
        put(39, "Semicolon"); put(40, "Quote"); put(41, "Backquote"); put(42, "ShiftLeft")
        put(43, "Backslash")
        "ZXCVBNM".forEachIndexed { i, c -> put(44 + i, "Key$c") }
        put(51, "Comma"); put(52, "Period"); put(53, "Slash"); put(54, "ShiftRight")
        put(55, "NumpadMultiply"); put(56, "AltLeft"); put(57, "Space"); put(58, "CapsLock")
        for (i in 0 until 10) put(59 + i, "F${i + 1}")
        put(69, "NumLock"); put(70, "ScrollLock")
        put(71, "Numpad7"); put(72, "Numpad8"); put(73, "Numpad9"); put(74, "NumpadSubtract")
        put(75, "Numpad4"); put(76, "Numpad5"); put(77, "Numpad6"); put(78, "NumpadAdd")
        put(79, "Numpad1"); put(80, "Numpad2"); put(81, "Numpad3"); put(82, "Numpad0")
        put(83, "NumpadDecimal"); put(86, "IntlBackslash"); put(87, "F11"); put(88, "F12")
        put(96, "NumpadEnter"); put(97, "ControlRight"); put(98, "NumpadDivide")
        put(99, "PrintScreen"); put(100, "AltRight"); put(102, "Home"); put(103, "ArrowUp")
        put(104, "PageUp"); put(105, "ArrowLeft"); put(106, "ArrowRight"); put(107, "End")
        put(108, "ArrowDown"); put(109, "PageDown"); put(110, "Insert"); put(111, "Delete")
        put(119, "Pause"); put(125, "MetaLeft"); put(126, "MetaRight"); put(127, "ContextMenu")
        for (i in 0 until 12) put(183 + i, "F${i + 13}")
    }

    private val keycodes: Map<Int, String> = buildMap {
        for (i in 0 until 26) put(KeyEvent.KEYCODE_A + i, "Key" + ('A' + i))
        for (i in 0 until 10) {
            put(KeyEvent.KEYCODE_0 + i, "Digit$i")
            put(KeyEvent.KEYCODE_NUMPAD_0 + i, "Numpad$i")
        }
        for (i in 0 until 12) put(KeyEvent.KEYCODE_F1 + i, "F${i + 1}")
        put(KeyEvent.KEYCODE_ENTER, "Enter"); put(KeyEvent.KEYCODE_DEL, "Backspace")
        put(KeyEvent.KEYCODE_FORWARD_DEL, "Delete"); put(KeyEvent.KEYCODE_TAB, "Tab")
        put(KeyEvent.KEYCODE_SPACE, "Space"); put(KeyEvent.KEYCODE_ESCAPE, "Escape")
        put(KeyEvent.KEYCODE_DPAD_UP, "ArrowUp"); put(KeyEvent.KEYCODE_DPAD_DOWN, "ArrowDown")
        put(KeyEvent.KEYCODE_DPAD_LEFT, "ArrowLeft"); put(KeyEvent.KEYCODE_DPAD_RIGHT, "ArrowRight")
        put(KeyEvent.KEYCODE_MOVE_HOME, "Home"); put(KeyEvent.KEYCODE_MOVE_END, "End")
        put(KeyEvent.KEYCODE_PAGE_UP, "PageUp"); put(KeyEvent.KEYCODE_PAGE_DOWN, "PageDown")
        put(KeyEvent.KEYCODE_INSERT, "Insert")
        put(KeyEvent.KEYCODE_CTRL_LEFT, "ControlLeft"); put(KeyEvent.KEYCODE_CTRL_RIGHT, "ControlRight")
        put(KeyEvent.KEYCODE_SHIFT_LEFT, "ShiftLeft"); put(KeyEvent.KEYCODE_SHIFT_RIGHT, "ShiftRight")
        put(KeyEvent.KEYCODE_ALT_LEFT, "AltLeft"); put(KeyEvent.KEYCODE_ALT_RIGHT, "AltRight")
        put(KeyEvent.KEYCODE_META_LEFT, "MetaLeft"); put(KeyEvent.KEYCODE_META_RIGHT, "MetaRight")
        put(KeyEvent.KEYCODE_CAPS_LOCK, "CapsLock"); put(KeyEvent.KEYCODE_SCROLL_LOCK, "ScrollLock")
        put(KeyEvent.KEYCODE_BREAK, "Pause"); put(KeyEvent.KEYCODE_SYSRQ, "PrintScreen")
        put(KeyEvent.KEYCODE_MENU, "ContextMenu"); put(KeyEvent.KEYCODE_NUM_LOCK, "NumLock")
        put(KeyEvent.KEYCODE_NUMPAD_DIVIDE, "NumpadDivide"); put(KeyEvent.KEYCODE_NUMPAD_MULTIPLY, "NumpadMultiply")
        put(KeyEvent.KEYCODE_NUMPAD_SUBTRACT, "NumpadSubtract"); put(KeyEvent.KEYCODE_NUMPAD_ADD, "NumpadAdd")
        put(KeyEvent.KEYCODE_NUMPAD_DOT, "NumpadDecimal"); put(KeyEvent.KEYCODE_NUMPAD_ENTER, "NumpadEnter")
        put(KeyEvent.KEYCODE_MINUS, "Minus"); put(KeyEvent.KEYCODE_EQUALS, "Equal")
        put(KeyEvent.KEYCODE_LEFT_BRACKET, "BracketLeft"); put(KeyEvent.KEYCODE_RIGHT_BRACKET, "BracketRight")
        put(KeyEvent.KEYCODE_BACKSLASH, "Backslash"); put(KeyEvent.KEYCODE_SEMICOLON, "Semicolon")
        put(KeyEvent.KEYCODE_APOSTROPHE, "Quote"); put(KeyEvent.KEYCODE_SLASH, "Slash")
        put(KeyEvent.KEYCODE_GRAVE, "Backquote"); put(KeyEvent.KEYCODE_COMMA, "Comma")
        put(KeyEvent.KEYCODE_PERIOD, "Period")
    }
}
