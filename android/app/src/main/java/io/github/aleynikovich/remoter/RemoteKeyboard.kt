package io.github.aleynikovich.remoter

import android.view.KeyCharacterMap
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import kotlin.math.min

/**
 * Turns hardware keys, the on-screen keyboard and the special-keys bar into
 * remote key events.
 *
 * Hardware keys are sent by physical position (like the browser viewer), so
 * the remote layout decides what they type. Text from the on-screen keyboard
 * is sent as characters, so it types exactly what was entered whatever the
 * remote layout is.
 *
 * The bar's Ctrl, Alt, Shift and Win keys latch: tap one and it applies to the
 * next key or click; tap it again to send it alone (Win opens Start); hold it
 * to lock it on.
 */
class RemoteKeyboard(
    /** Sends keydown or keyup with a KeyboardEvent.key and .code. */
    private val send: (kind: String, key: String, code: String) -> Unit,
) {
    enum class Latch { OFF, ONCE, LOCKED }

    val latches = linkedMapOf(
        "ControlLeft" to Latch.OFF,
        "AltLeft" to Latch.OFF,
        "ShiftLeft" to Latch.OFF,
        "MetaLeft" to Latch.OFF,
    )

    /** Called when a latch changes, to update the bar. */
    var onLatchChanged: () -> Unit = {}

    /** Ctrl+Alt+Esc on a hardware keyboard: get the mouse back. */
    var onReleaseCombo: () -> Unit = {}

    private val held = linkedSetOf<String>()
    private var mouseMods = emptyList<String>()

    private fun down(code: String, key: String = "") = send("keydown", key, code)
    private fun up(code: String, key: String = "") = send("keyup", key, code)

    private fun latched(): List<String> = latches.filterValues { it != Latch.OFF }.keys.toList()

    private fun clearOnce() {
        var changed = false
        for ((k, v) in latches) if (v == Latch.ONCE) {
            latches[k] = Latch.OFF
            changed = true
        }
        if (changed) onLatchChanged()
    }

    /** Runs [block] with the latched modifiers (and Shift, if [shift]) held down. */
    private fun withMods(shift: Boolean = false, block: () -> Unit) {
        val mods = latched().toMutableList()
        if (shift && "ShiftLeft" !in mods) mods.add("ShiftLeft")
        mods.forEach { down(it) }
        block()
        mods.asReversed().forEach { up(it) }
        clearOnce()
    }

    /** Presses and releases a key, with the latched modifiers. */
    fun tap(code: String) = withMods {
        down(code)
        up(code)
    }

    /** A modifier on the bar was tapped. */
    fun tapModifier(code: String) {
        when (latches[code]) {
            Latch.OFF -> latches[code] = Latch.ONCE
            Latch.ONCE -> {
                // Tapped twice: the key on its own, e.g. Win for the Start menu.
                latches[code] = Latch.OFF
                val others = latched()
                others.forEach { down(it) }
                down(code); up(code)
                others.asReversed().forEach { up(it) }
                clearOnce()
            }
            else -> latches[code] = Latch.OFF
        }
        onLatchChanged()
    }

    /** A modifier on the bar was held: keep it on until tapped. */
    fun lockModifier(code: String) {
        latches[code] = Latch.LOCKED
        onLatchChanged()
    }

    /** Types text from the on-screen keyboard. */
    fun type(text: String) {
        for (c in text) {
            when {
                c == '\n' -> tap("Enter")
                c == '\t' -> tap("Tab")
                latched().isNotEmpty() -> {
                    // A shortcut such as Ctrl+C needs the real key, not the character.
                    val mapped = Keys.codeForChar(c)
                    if (mapped != null) {
                        withMods(mapped.second) { down(mapped.first); up(mapped.first) }
                    } else {
                        withMods { down("", c.toString()); up("", c.toString()) }
                    }
                }
                else -> {
                    down("", c.toString())
                    up("", c.toString())
                }
            }
        }
    }

    /** A key event the on-screen keyboard sent instead of text. */
    fun imeKey(e: KeyEvent) {
        if (e.action != KeyEvent.ACTION_DOWN) return
        val uc = e.getUnicodeChar(e.metaState)
        if (uc > 0 && uc and KeyCharacterMap.COMBINING_ACCENT == 0 && !e.isCtrlPressed && !e.isAltPressed) {
            type(uc.toChar().toString())
            return
        }
        val code = Keys.codeForKeycode(e.keyCode) ?: return
        val extra = buildList {
            if (e.isCtrlPressed) add("ControlLeft")
            if (e.isAltPressed) add("AltLeft")
            if (e.isShiftPressed) add("ShiftLeft")
            if (e.isMetaPressed) add("MetaLeft")
        }.filter { latches[it] == Latch.OFF }
        extra.forEach { down(it) }
        tap(code)
        extra.asReversed().forEach { up(it) }
    }

    /**
     * A key from a hardware keyboard (or the phone's own buttons). Returns
     * false for keys the phone should keep, such as volume.
     */
    fun hardware(e: KeyEvent): Boolean {
        when (e.keyCode) {
            KeyEvent.KEYCODE_VOLUME_UP, KeyEvent.KEYCODE_VOLUME_DOWN, KeyEvent.KEYCODE_VOLUME_MUTE,
            KeyEvent.KEYCODE_POWER, KeyEvent.KEYCODE_CAMERA, KeyEvent.KEYCODE_APP_SWITCH,
            KeyEvent.KEYCODE_HOME -> return false
        }
        val device = e.device
        if (device == null || device.isVirtual || e.deviceId == KeyCharacterMap.VIRTUAL_KEYBOARD) return false
        if (!device.isFullKeyboard) {
            // A mouse's back button arrives as BACK; swallow it rather than
            // leaving the session.
            return e.keyCode == KeyEvent.KEYCODE_BACK
        }
        val code = Keys.codeForScancode(e.scanCode) ?: Keys.codeForKeycode(e.keyCode) ?: return false
        when (e.action) {
            KeyEvent.ACTION_DOWN -> {
                if (code == "Escape" && e.isCtrlPressed && e.isAltPressed) {
                    releaseAll()
                    onReleaseCombo()
                    return true
                }
                held.add(code)
                down(code)
            }
            KeyEvent.ACTION_UP -> {
                if (held.remove(code)) up(code)
            }
        }
        return true
    }

    /** Clicks with a latched modifier become Ctrl+click and so on. */
    fun mouseDown() {
        mouseMods = latched()
        mouseMods.forEach { down(it) }
    }

    fun mouseUp() {
        mouseMods.asReversed().forEach { up(it) }
        if (mouseMods.isNotEmpty()) clearOnce()
        mouseMods = emptyList()
    }

    /** Lets go of every key held on a hardware keyboard, e.g. when the app loses focus. */
    fun releaseAll() {
        held.toList().asReversed().forEach { up(it) }
        held.clear()
    }

    /** The connection the on-screen keyboard types into. */
    fun inputConnection(view: View, info: EditorInfo): InputConnection {
        // No suggestions and no composing where the keyboard allows it: what is
        // typed goes out right away, key by key.
        info.inputType = EditorInfo.TYPE_CLASS_TEXT or
            EditorInfo.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD or
            EditorInfo.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        info.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or
            EditorInfo.IME_FLAG_NO_EXTRACT_UI or
            EditorInfo.IME_ACTION_NONE or
            EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
        info.initialSelStart = 0
        info.initialSelEnd = 0
        return Connection(view)
    }

    /**
     * Keyboards that still compose words (or replace them with a suggestion)
     * are followed by diffing: the part that changed is erased with Backspace
     * and retyped.
     */
    private inner class Connection(view: View) : BaseInputConnection(view, false) {
        private var composing = ""

        private fun replace(text: String) {
            var p = 0
            while (p < min(text.length, composing.length) && text[p] == composing[p]) p++
            repeat(composing.length - p) { tap("Backspace") }
            type(text.substring(p))
        }

        override fun setComposingText(text: CharSequence, newCursorPosition: Int): Boolean {
            val t = text.toString()
            replace(t)
            composing = t
            return true
        }

        override fun commitText(text: CharSequence, newCursorPosition: Int): Boolean {
            replace(text.toString())
            composing = ""
            return true
        }

        override fun finishComposingText(): Boolean {
            composing = ""
            return true
        }

        override fun setComposingRegion(start: Int, end: Int) = true

        override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean {
            if (composing.isNotEmpty()) composing = composing.dropLast(min(beforeLength, composing.length))
            repeat(beforeLength) { tap("Backspace") }
            repeat(afterLength) { tap("Delete") }
            return true
        }

        override fun deleteSurroundingTextInCodePoints(beforeLength: Int, afterLength: Int) =
            deleteSurroundingText(beforeLength, afterLength)

        override fun sendKeyEvent(event: KeyEvent): Boolean {
            imeKey(event)
            return true
        }

        override fun performEditorAction(actionCode: Int): Boolean {
            tap("Enter")
            return true
        }

        override fun getTextBeforeCursor(length: Int, flags: Int): CharSequence = composing.takeLast(length)

        override fun getTextAfterCursor(length: Int, flags: Int): CharSequence = ""
    }
}
