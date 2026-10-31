package io.github.aleynikovich.remoter

import android.util.Log
import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.net.SocketTimeoutException

/** A Remoter that answered on the home network (see internal/lan in the Go code). */
data class LanAnswer(
    val name: String,
    val os: String,
    val ip: String,
    val port: Int,
    val remoter: String,
    val share: Boolean,
    val tailnet: String,
) {
    /** host:port to connect to, like the saved computers. */
    val addr get() = "$ip:$port"
}

/**
 * Finds Remoters on the same Wi-Fi without Tailscale: broadcast a question
 * to UDP 8080 and every Remoter there answers with who it is.
 */
object Lan {
    const val PORT = 8080
    private val QUESTION = "REMOTER?".toByteArray()
    private const val TAG = "RemoterLan"

    /** Home, office and link-local addresses, and this phone; Tailscale's range does not count. */
    fun isLocal(a: InetAddress): Boolean {
        if (a.isLoopbackAddress || a.isSiteLocalAddress || a.isLinkLocalAddress) return true
        return a is Inet6Address && (a.address[0].toInt() and 0xfe) == 0xfc // fc00::/7
    }

    /** Where to send the question: each Wi-Fi or cable network, and everyone. */
    private fun broadcasts(): List<InetAddress> {
        val out = mutableListOf<InetAddress>(InetAddress.getByName("255.255.255.255"))
        runCatching {
            for (ifc in NetworkInterface.getNetworkInterfaces()) {
                if (!ifc.isUp || ifc.isLoopback) continue
                for (ia in ifc.interfaceAddresses) {
                    val b = ia.broadcast ?: continue
                    if (ia.address is Inet4Address && ia.address.isSiteLocalAddress && b !in out) out.add(b)
                }
            }
        }
        return out
    }

    /** Every IPv4 address this phone has, so it does not list itself. */
    fun ownIps(): Set<String> = runCatching {
        NetworkInterface.getNetworkInterfaces().toList().flatMap { it.inetAddresses.toList() }
            .filterIsInstance<Inet4Address>().mapNotNull { it.hostAddress }.toSet()
    }.getOrDefault(emptySet())

    /** This phone's Tailscale IPv4 address, or "". */
    fun tailscaleIp(): String = ShareClient.ownAddresses().firstOrNull() ?: ""

    /** Asks the network and collects answers for [waitMs], one per computer. Blocking. */
    fun discover(waitMs: Int = 1200): List<LanAnswer> {
        val found = mutableListOf<LanAnswer>()
        try {
            DatagramSocket().use { sock ->
                sock.broadcast = true
                val targets = broadcasts()
                val deadline = System.currentTimeMillis() + waitMs
                var asked = 0
                val buf = ByteArray(2048)
                while (true) {
                    val now = System.currentTimeMillis()
                    if (now >= deadline) break
                    // Ask twice: Wi-Fi drops broadcasts more than anything else.
                    if (asked < 2 && now >= deadline - waitMs + asked * waitMs / 3) {
                        for (t in targets) runCatching { sock.send(DatagramPacket(QUESTION, QUESTION.size, t, PORT)) }
                        asked++
                    }
                    sock.soTimeout = maxOf(1, minOf(deadline - now, (waitMs / 3).toLong()).toInt())
                    val p = DatagramPacket(buf, buf.size)
                    try {
                        sock.receive(p)
                    } catch (e: SocketTimeoutException) {
                        continue
                    }
                    val a = runCatching {
                        val o = JSONObject(String(p.data, 0, p.length))
                        LanAnswer(
                            name = o.optString("name"),
                            os = o.optString("os"),
                            ip = p.address.hostAddress ?: return@runCatching null,
                            port = o.optInt("port", PORT).takeIf { it > 0 } ?: PORT,
                            remoter = o.optString("remoter"),
                            share = o.optBoolean("share"),
                            tailnet = o.optString("tailnet"),
                        )
                    }.getOrNull() ?: continue
                    // A PC on both cable and Wi-Fi answers on each; keep one.
                    if (found.none { it.ip == a.ip || (it.name == a.name && it.os == a.os) }) found.add(a)
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "discovery failed: $e")
        }
        return found
    }

    /** Answers PCs looking for this phone, so they can send it files without Tailscale. */
    class Responder(private val info: () -> JSONObject) {
        @Volatile private var sock: DatagramSocket? = null

        fun start() {
            if (sock != null) return
            val s = try {
                DatagramSocket(null).apply {
                    reuseAddress = true
                    bind(InetSocketAddress(PORT))
                }
            } catch (e: Exception) {
                Log.w(TAG, "cannot answer on UDP $PORT: $e")
                return
            }
            sock = s
            Thread({
                val buf = ByteArray(64)
                while (!s.isClosed) {
                    val p = DatagramPacket(buf, buf.size)
                    try {
                        s.receive(p)
                    } catch (e: Exception) {
                        break
                    }
                    if (!isLocal(p.address) || !p.data.copyOf(p.length).contentEquals(QUESTION)) continue
                    val b = info().toString().toByteArray()
                    runCatching { s.send(DatagramPacket(b, b.size, p.socketAddress)) }
                }
            }, "remoter-lan").apply { isDaemon = true }.start()
        }

        fun stop() {
            sock?.close()
            sock = null
        }
    }
}
