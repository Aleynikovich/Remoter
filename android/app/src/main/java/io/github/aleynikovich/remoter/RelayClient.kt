package io.github.aleynikovich.remoter

import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/**
 * A viewer connection to one host through a Remoter relay, speaking the same
 * WebSocket protocol as the browser viewer (see internal/protocol in the Go
 * code): JSON control messages, binary screen updates.
 *
 * A session that drops (the host updating itself, Wi-Fi to mobile handover) is
 * retried for [RECONNECT_MS] before the client gives up, like the other viewers.
 */
class RelayClient(
    server: String,
    private val hostId: String,
    private val password: String,
    private val listener: Listener,
) {
    interface Listener {
        /** The host accepted the password. Main thread. */
        fun onConnected(sessionId: String)

        /** A binary screen update. Called on the network thread, in order. */
        fun onFrame(data: ByteArray)

        /** A control message from the host (cursor, displays, clipboard). Main thread. */
        fun onMessage(msg: JSONObject)

        /** A passing state such as reconnecting, or null when it is over. Main thread. */
        fun onStatus(text: String?)

        /** The session is over for good. Main thread. */
        fun onEnded(reason: String, wrongPassword: Boolean)
    }

    val url: String = viewerUrl(server)

    private val http = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)
        .pingInterval(10, TimeUnit.SECONDS)
        .build()
    private val main = Handler(Looper.getMainLooper())

    @Volatile private var ws: WebSocket? = null
    @Volatile var connected = false
        private set
    @Volatile private var closed = false
    private var everConnected = false
    private var lostAt = 0L
    private var lastError = ""
    private var refused = false

    fun start() {
        open()
    }

    fun close() {
        closed = true
        connected = false
        ws?.close(1000, null)
        ws = null
        main.removeCallbacksAndMessages(null)
    }

    private fun open() {
        lastError = ""
        refused = false
        val req = Request.Builder().url(url).build()
        ws = http.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                val hello = JSONObject().put("type", "connect").put("id", hostId).put("password", password)
                webSocket.send(hello.toString())
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (webSocket !== ws) return
                val m = try {
                    JSONObject(text)
                } catch (e: Exception) {
                    return
                }
                when (m.optString("type")) {
                    "connected" -> {
                        connected = true
                        everConnected = true
                        val id = m.optString("id")
                        main.post { if (!closed) { listener.onStatus(null); listener.onConnected(id) } }
                    }
                    "error" -> {
                        lastError = m.optString("error")
                        refused = !connected
                    }
                    else -> main.post { if (!closed) listener.onMessage(m) }
                }
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                if (webSocket === ws && connected) listener.onFrame(bytes.toByteArray())
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
                dropped(webSocket)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = dropped(webSocket)

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = dropped(webSocket)
        })
    }

    private fun dropped(socket: WebSocket) {
        main.post {
            if (socket !== ws || closed) return@post
            ws = null
            val wasConnected = connected
            connected = false
            val now = SystemClock.elapsedRealtime()
            if (wasConnected) lostAt = now
            if (!everConnected || refused || now - lostAt > RECONNECT_MS) {
                val text = lastError.ifEmpty { if (everConnected) "Session ended." else "Could not reach the server." }
                closed = true
                listener.onEnded(text, lastError == "wrong password")
                return@post
            }
            listener.onStatus("Connection lost, reconnecting…")
            main.postDelayed({ if (!closed && ws == null) open() }, 1000)
        }
    }

    /** Sends a control message. Messages sent while not connected are dropped. */
    fun send(m: JSONObject) {
        if (connected) ws?.send(m.toString())
    }

    /**
     * Sends a mouse move unless the link is badly behind, in which case it is
     * dropped: the next one carries the newer position anyway.
     */
    fun sendMove(m: JSONObject): Boolean {
        val socket = ws ?: return false
        if (!connected || socket.queueSize() > 32 * 1024) return false
        return socket.send(m.toString())
    }

    fun ack(seq: Long) = send(JSONObject().put("type", "ack").put("seq", seq))

    fun refresh(video: Boolean) = send(JSONObject().put("type", "refresh").put("video", video))

    fun input(kind: String, fill: JSONObject.() -> Unit = {}) {
        val input = JSONObject().put("kind", kind).apply(fill)
        val m = JSONObject().put("type", "input").put("input", input)
        if (kind == "mousemove") sendMove(m) else send(m)
    }

    companion object {
        const val RECONNECT_MS = 60_000L

        /**
         * Turns what a person types ("100.1.2.3:8080", "http://relay",
         * "wss://relay/ws/viewer") into the relay's viewer WebSocket URL.
         * OkHttp wants http(s) schemes for WebSockets.
         */
        fun viewerUrl(server: String): String {
            var s = server.trim()
            require(s.isNotEmpty()) { "Enter the computer's address" }
            if (!s.contains("://")) s = "http://$s"
            val scheme = s.substringBefore("://").lowercase()
            var rest = s.substringAfter("://")
            val secure = when (scheme) {
                "http", "ws" -> false
                "https", "wss" -> true
                else -> throw IllegalArgumentException("Bad address $server")
            }
            if (!rest.contains('/') || rest.substringAfter('/').isEmpty()) {
                rest = rest.trimEnd('/')
                if (!rest.substringAfterLast(']').contains(':') && !secure) rest += ":8080"
                rest += "/ws/viewer"
            }
            return (if (secure) "https://" else "http://") + rest
        }

        /** The relay's base http(s) URL, for GET /version. */
        fun baseUrl(server: String): String = viewerUrl(server).substringBefore("/ws/viewer")
    }
}
