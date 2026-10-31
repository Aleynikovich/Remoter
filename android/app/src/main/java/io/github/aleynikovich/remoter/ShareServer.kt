package io.github.aleynikovich.remoter

import org.json.JSONObject
import java.io.BufferedInputStream
import java.io.EOFException
import java.io.FilterInputStream
import java.io.IOException
import java.io.InputStream
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.URLDecoder
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/**
 * The receiving half of Remoter file sharing, the same small HTTP API every
 * Remoter serves (internal/share in the Go code), on [PORT]:
 *
 *     GET  /share/info              {"name": "...", "os": "android", "version": "..."}
 *     POST /share/file?name=a.pdf   body = the file
 *     POST /share/text              body = text or a link
 *
 * Senders name themselves in X-Remoter-From; batches of files are numbered
 * with X-Remoter-Index / X-Remoter-Count. Only devices on the tailnet or home network (and
 * this phone) may connect. One request per connection.
 */
class ShareServer(
    private val info: () -> JSONObject,
    private val receiver: Receiver,
    private val port: Int = PORT,
) {
    interface Receiver {
        /** Stores one incoming file and returns where it went (its content URI or name). Network thread. */
        fun saveFile(from: String, name: String, size: Long, body: InputStream): String

        /** All files of one batch from [from] have arrived. */
        fun filesDone(from: String, saved: List<String>)

        fun text(from: String, text: String)
    }

    private var socket: ServerSocket? = null
    private var pool: ExecutorService? = null
    private val batches = HashMap<String, MutableList<String>>()

    @Volatile var error: String? = null
        private set

    /** Starts listening; returns false (with [error] set) if the port cannot be had. */
    @Synchronized
    fun start(): Boolean {
        if (socket != null) return true
        val s = try {
            ServerSocket().apply {
                reuseAddress = true
                bind(InetSocketAddress(port))
            }
        } catch (e: IOException) {
            error = "Cannot listen on port $port: ${e.message}"
            return false
        }
        error = null
        socket = s
        val p = Executors.newCachedThreadPool()
        pool = p
        Thread({
            while (!s.isClosed) {
                val c = try {
                    s.accept()
                } catch (e: IOException) {
                    break
                }
                p.execute { serve(c) }
            }
        }, "share-accept").apply { isDaemon = true }.start()
        return true
    }

    @Synchronized
    fun stop() {
        socket?.close()
        socket = null
        pool?.shutdownNow()
        pool = null
    }

    private fun serve(c: Socket) {
        c.use { sock ->
            sock.soTimeout = 60_000
            val input = BufferedInputStream(sock.getInputStream(), 64 * 1024)
            val out = sock.getOutputStream()
            fun reply(code: Int, reason: String, body: String = "", type: String = "text/plain; charset=utf-8") {
                val b = body.toByteArray()
                out.write(("HTTP/1.1 $code $reason\r\nContent-Type: $type\r\nContent-Length: ${b.size}\r\n" +
                    "Cache-Control: no-store\r\nConnection: close\r\n\r\n").toByteArray())
                out.write(b)
                out.flush()
            }
            try {
                val req = Request.read(input) ?: return
                if (!allowed(sock.inetAddress)) {
                    reply(403, "Forbidden", "only for devices on your Tailscale network")
                    return
                }
                val from = req.headers["x-remoter-from"]?.trim()?.take(64)?.ifEmpty { null }
                if (req.method != "GET" && from == null) {
                    // Keeps web pages in some browser from posting here.
                    reply(400, "Bad Request", "missing X-Remoter-From header")
                    return
                }
                when ("${req.method} ${req.path}") {
                    "GET /share/info" -> reply(200, "OK", info().toString(), "application/json")
                    "POST /share/file" -> {
                        val name = safeName(req.query["name"] ?: "")
                        val saved = receiver.saveFile(from!!, name, req.contentLength, req.body(input))
                        reply(204, "No Content")
                        val index = req.headers["x-remoter-index"]?.toIntOrNull() ?: 0
                        val count = req.headers["x-remoter-count"]?.toIntOrNull() ?: 0
                        val done: List<String>?
                        synchronized(batches) {
                            if (index <= 1) batches.remove(from)
                            val batch = batches.getOrPut(from) { mutableListOf() }
                            batch.add(saved)
                            done = if (count <= 1 || index >= count) batches.remove(from) else null
                        }
                        if (done != null) receiver.filesDone(from, done)
                    }
                    "POST /share/text" -> {
                        val bytes = req.body(input).readLimited(MAX_TEXT)
                        val text = String(bytes, Charsets.UTF_8).trim()
                        if (text.isEmpty()) {
                            reply(400, "Bad Request", "empty text")
                        } else {
                            reply(204, "No Content")
                            receiver.text(from!!, text)
                        }
                    }
                    else -> reply(404, "Not Found", "not found")
                }
            } catch (e: Exception) {
                runCatching { reply(500, "Internal Server Error", e.message ?: e.toString()) }
            }
        }
    }

    class Request(
        val method: String,
        val path: String,
        val query: Map<String, String>,
        val headers: Map<String, String>,
    ) {
        val contentLength: Long get() = headers["content-length"]?.toLongOrNull() ?: -1
        private val chunked get() = headers["transfer-encoding"]?.contains("chunked", true) == true

        fun body(input: InputStream): InputStream = when {
            chunked -> ChunkedInputStream(input)
            contentLength >= 0 -> LimitedInputStream(input, contentLength)
            else -> LimitedInputStream(input, 0)
        }

        companion object {
            /** Reads the request line and headers; null if the client went away first. */
            fun read(input: InputStream): Request? {
                val line = readLine(input) ?: return null
                val parts = line.split(" ")
                if (parts.size < 2) throw IOException("bad request line")
                val headers = HashMap<String, String>()
                var count = 0
                while (true) {
                    val h = readLine(input) ?: throw EOFException()
                    if (h.isEmpty()) break
                    if (++count > 100) throw IOException("too many headers")
                    val i = h.indexOf(':')
                    if (i > 0) headers[h.substring(0, i).trim().lowercase()] = h.substring(i + 1).trim()
                }
                val target = parts[1]
                val q = target.indexOf('?')
                val path = if (q < 0) target else target.substring(0, q)
                val query = HashMap<String, String>()
                if (q >= 0) {
                    for (kv in target.substring(q + 1).split('&')) {
                        if (kv.isEmpty()) continue
                        val e = kv.indexOf('=')
                        val k = if (e < 0) kv else kv.substring(0, e)
                        val v = if (e < 0) "" else kv.substring(e + 1)
                        query[URLDecoder.decode(k, "UTF-8")] = URLDecoder.decode(v, "UTF-8")
                    }
                }
                return Request(parts[0], path, query, headers)
            }

            internal fun readLine(input: InputStream): String? {
                val sb = StringBuilder()
                while (true) {
                    val b = input.read()
                    if (b < 0) return if (sb.isEmpty()) null else throw EOFException()
                    if (b == '\n'.code) return sb.toString().trimEnd('\r')
                    if (sb.length > 8192) throw IOException("line too long")
                    sb.append(b.toChar())
                }
            }
        }
    }

    private class LimitedInputStream(input: InputStream, private var left: Long) : FilterInputStream(input) {
        override fun read(): Int {
            if (left <= 0) return -1
            val b = super.read()
            if (b < 0) throw EOFException("connection closed early")
            left--
            return b
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (left <= 0) return -1
            val n = super.read(b, off, minOf(len.toLong(), left).toInt())
            if (n < 0) throw EOFException("connection closed early")
            left -= n
            return n
        }

        override fun close() {}
    }

    /** Transfer-Encoding: chunked, as Go sends a body of unknown length. */
    private class ChunkedInputStream(private val input: InputStream) : InputStream() {
        private var left = 0L
        private var done = false

        private fun next(): Boolean {
            if (done) return false
            if (left == 0L) {
                val line = Request.readLine(input) ?: throw EOFException()
                left = line.substringBefore(';').trim().toLong(16)
                if (left == 0L) {
                    while ((Request.readLine(input) ?: "").isNotEmpty()) { /* trailers */ }
                    done = true
                    return false
                }
            }
            return true
        }

        override fun read(): Int {
            val one = ByteArray(1)
            return if (read(one, 0, 1) < 0) -1 else one[0].toInt() and 0xff
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (!next()) return -1
            val n = input.read(b, off, minOf(len.toLong(), left).toInt())
            if (n < 0) throw EOFException("connection closed early")
            left -= n
            if (left == 0L) Request.readLine(input) // the CRLF after each chunk
            return n
        }
    }

    companion object {
        const val PORT = 8080
        const val MAX_TEXT = 1 shl 20

        /** Tailscale addresses (100.64.0.0/10, fd7a:115c:a1e0::/48), the home network and this phone. */
        fun allowed(a: InetAddress): Boolean {
            if (Lan.isLocal(a)) return true
            val b = a.address
            return when (a) {
                is Inet4Address -> (b[0].toInt() and 0xff) == 100 && (b[1].toInt() and 0xc0) == 64
                is Inet6Address -> {
                    // An IPv4 address mapped into IPv6 (::ffff:100.x.y.z).
                    val mapped = b.take(10).all { it == 0.toByte() } && b[10] == 0xff.toByte() && b[11] == 0xff.toByte()
                    if (mapped) (b[12].toInt() and 0xff) == 100 && (b[13].toInt() and 0xc0) == 64
                    else (b[0].toInt() and 0xff) == 0xfd && (b[1].toInt() and 0xff) == 0x7a && (b[2].toInt() and 0xff) == 0x11 &&
                        (b[3].toInt() and 0xff) == 0x5c && (b[4].toInt() and 0xff) == 0xa1 && (b[5].toInt() and 0xff) == 0xe0
                }
                else -> false
            }
        }

        /** Same rules as share.SafeName in the Go code: no folders, nothing hidden or empty. */
        fun safeName(raw: String): String {
            var n = raw.replace('\\', '/').substringAfterLast('/')
            n = n.map { if (it.isISOControl() || it in "<>:\"|?*") '_' else it }.joinToString("")
            n = n.trim().trimStart('.').trimEnd('.', ' ')
            if (n.isEmpty()) return "shared file"
            if (n.length > 200) {
                val ext = n.substringAfterLast('.', "").let { if (it.isEmpty() || it.length > 19) "" else ".$it" }
                n = n.take(200 - ext.length) + ext
            }
            return n
        }

        private fun InputStream.readLimited(max: Int): ByteArray {
            val out = java.io.ByteArrayOutputStream()
            val buf = ByteArray(8192)
            while (true) {
                val n = read(buf)
                if (n < 0) break
                out.write(buf, 0, n)
                if (out.size() > max) throw IOException("text too long, send it as a file")
            }
            return out.toByteArray()
        }
    }
}
