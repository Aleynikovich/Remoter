package io.github.aleynikovich.remoter

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ProtocolTest {
    @Test
    fun viewerUrl() {
        assertEquals("http://100.102.208.35:8080/ws/viewer", RelayClient.viewerUrl("100.102.208.35:8080"))
        assertEquals("http://100.102.208.35:8080/ws/viewer", RelayClient.viewerUrl(" 100.102.208.35 "))
        assertEquals("http://pc:9000/ws/viewer", RelayClient.viewerUrl("ws://pc:9000/"))
        assertEquals("https://relay.example/ws/viewer", RelayClient.viewerUrl("https://relay.example"))
        assertEquals("http://pc:8080/custom", RelayClient.viewerUrl("http://pc:8080/custom"))
        assertEquals("http://pc:8080", RelayClient.baseUrl("pc"))
    }

    @Test
    fun parameterSets() {
        val sps = byteArrayOf(0x67, 0x42, 0x00, 0x1f)
        val pps = byteArrayOf(0x68, 0xce.toByte(), 0x3c)
        val idr = byteArrayOf(0x65, 0x11, 0x22)
        val frame = byteArrayOf(0, 0, 0, 1) + sps + byteArrayOf(0, 0, 0, 1) + pps + byteArrayOf(0, 0, 1) + idr
        val (s, p) = VideoDecoder.parameterSets(frame)
        assertArrayEquals(byteArrayOf(0, 0, 0, 1) + sps, s)
        assertArrayEquals(byteArrayOf(0, 0, 0, 1) + pps, p)
        val (s2, p2) = VideoDecoder.parameterSets(byteArrayOf(0, 0, 1) + idr)
        assertNull(s2)
        assertNull(p2)
    }

    @Test
    fun littleEndian() {
        val b = byteArrayOf(0x56, 0x04, 0x03, 0x02, 0xf1.toByte(), 0x80.toByte(), 0x07, 0x38, 0x04)
        assertEquals(0xf1020304L, VideoDecoder.u32(b, 1))
        assertEquals(1920, VideoDecoder.u16(b, 5))
        assertEquals(1080, VideoDecoder.u16(b, 7))
    }

    @Test
    fun keys() {
        assertEquals("KeyA", Keys.codeForScancode(30))
        assertEquals("Escape", Keys.codeForScancode(1))
        assertEquals("MetaLeft", Keys.codeForScancode(125))
        assertEquals("F12", Keys.codeForScancode(88))
        assertEquals("ArrowUp", Keys.codeForScancode(103))
        assertEquals("KeyZ", Keys.codeForKeycode(54))
        assertEquals("Backspace", Keys.codeForKeycode(67))
        assertEquals("KeyC" to false, Keys.codeForChar('c'))
        assertEquals("KeyC" to true, Keys.codeForChar('C'))
        assertEquals("Digit1" to true, Keys.codeForChar('!'))
        assertNull(Keys.codeForChar('é'))
    }

    @Test
    fun latchedShortcut() {
        val sent = mutableListOf<String>()
        val kb = RemoteKeyboard { kind, key, code -> sent.add("$kind $code$key") }
        kb.tapModifier("ControlLeft")
        kb.type("c")
        kb.type("x")
        assertEquals(
            listOf("keydown ControlLeft", "keydown KeyC", "keyup KeyC", "keyup ControlLeft", "keydown x", "keyup x"),
            sent,
        )
    }
}
