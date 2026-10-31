package io.github.aleynikovich.remoter

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.Socket
import java.net.URL
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

class ShareServerTest {
    private class Got : ShareServer.Receiver {
        val files = LinkedBlockingQueue<String>()
        val batches = LinkedBlockingQueue<List<String>>()
        val texts = LinkedBlockingQueue<String>()
        override fun saveFile(from: String, name: String, size: Long, body: InputStream): String {
            val content = String(body.readBytes())
            files.add("$from|$name|$size|$content")
            return name
        }
        override fun filesDone(from: String, saved: List<String>) { batches.add(saved) }
        override fun text(from: String, text: String) { texts.add("$from|$text") }
    }

    private fun post(port: Int, path: String, body: String, headers: Map<String, String>): Int {
        val conn = URL("http://127.0.0.1:$port$path").openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        headers.forEach { (k, v) -> conn.setRequestProperty(k, v) }
        conn.outputStream.use { it.write(body.toByteArray()) }
        return conn.responseCode.also { conn.disconnect() }
    }

    @Test
    fun receivesFilesAndText() {
        val got = Got()
        val port = 18000 + (Math.random() * 1000).toInt()
        val server = ShareServer({ JSONObject() }, got, port)
        assertTrue(server.start())
        try {
            val from = mapOf("X-Remoter-From" to "sr00047l")
            assertEquals(204, post(port, "/share/file?name=My%20Report.pdf", "%PDF", from + mapOf("X-Remoter-Index" to "1", "X-Remoter-Count" to "2")))
            assertEquals("sr00047l|My Report.pdf|4|%PDF", got.files.poll(2, TimeUnit.SECONDS))
            assertEquals(null, got.batches.poll(100, TimeUnit.MILLISECONDS))

            // Chunked, as Go sends a body of unknown length.
            Socket("127.0.0.1", port).use { s ->
                s.getOutputStream().write(("POST /share/file?name=..%2F..%2Fx.jpg HTTP/1.1\r\nHost: x\r\nX-Remoter-From: sr00047l\r\n" +
                    "X-Remoter-Index: 2\r\nX-Remoter-Count: 2\r\nTransfer-Encoding: chunked\r\n\r\n" +
                    "3\r\nabc\r\n2;ext=1\r\nde\r\n0\r\n\r\n").toByteArray())
                val status = s.getInputStream().bufferedReader().readLine()
                assertEquals("HTTP/1.1 204 No Content", status)
            }
            assertEquals("sr00047l|x.jpg|-1|abcde", got.files.poll(2, TimeUnit.SECONDS))
            assertEquals(listOf("My Report.pdf", "x.jpg"), got.batches.poll(2, TimeUnit.SECONDS))

            assertEquals(204, post(port, "/share/text", "https://example.com/a", from))
            assertEquals("sr00047l|https://example.com/a", got.texts.poll(2, TimeUnit.SECONDS))

            // Web pages cannot add the sender header without asking first.
            assertEquals(400, post(port, "/share/text", "evil", emptyMap()))
            assertEquals(null, got.texts.poll(100, TimeUnit.MILLISECONDS))
        } finally {
            server.stop()
        }
    }

    @Test
    fun onlyTailnetAndHomeNetwork() {
        assertTrue(ShareServer.allowed(InetAddress.getByName("100.116.112.42")))
        assertTrue(ShareServer.allowed(InetAddress.getByName("127.0.0.1")))
        assertTrue(ShareServer.allowed(InetAddress.getByName("fd7a:115c:a1e0::1")))
        assertTrue(ShareServer.allowed(InetAddress.getByName("192.168.1.20")))
        assertTrue(ShareServer.allowed(InetAddress.getByName("10.0.0.5")))
        assertTrue(ShareServer.allowed(InetAddress.getByName("fe80::1")))
        assertFalse(ShareServer.allowed(InetAddress.getByName("100.128.0.1")))
        assertFalse(ShareServer.allowed(InetAddress.getByName("8.8.8.8")))
    }

    @Test
    fun safeNames() {
        assertEquals("passwd", ShareServer.safeName("../../etc/passwd"))
        assertEquals("photo.jpg", ShareServer.safeName("C:\\Users\\a\\photo.jpg"))
        assertEquals("shared file", ShareServer.safeName(".."))
        assertEquals("a_b.txt", ShareServer.safeName("a?b.txt"))
    }
}
