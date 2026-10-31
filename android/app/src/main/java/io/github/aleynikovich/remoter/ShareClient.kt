package io.github.aleynikovich.remoter

import android.content.Context
import android.net.Uri
import android.os.Build
import android.provider.OpenableColumns
import android.provider.Settings
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.Inet4Address
import java.net.NetworkInterface
import java.net.URL
import java.net.URLEncoder
import java.util.concurrent.Callable
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** A device that receives shares: a PC running Remoter, or another phone. */
data class ShareTarget(val name: String, val os: String, val ip: String) {
    val base get() = "http://$ip:${ShareServer.PORT}"

    fun toJson(): JSONObject = JSONObject().put("name", name).put("os", os).put("ip", ip)

    companion object {
        fun fromJson(o: JSONObject) = ShareTarget(o.optString("name"), o.optString("os"), o.optString("ip"))
    }
}

/** Sharing settings, kept apart from the viewer's. */
class SharePrefs(context: Context) {
    private val p = context.getSharedPreferences("share", Context.MODE_PRIVATE)

    /** Receive files and links from the PCs (runs [ShareService] in the background). */
    var receive: Boolean
        get() = p.getBoolean("receive", true)
        set(v) = p.edit().putBoolean("receive", v).apply()

    /** The devices found last time, shown at once while a fresh search runs. */
    var targets: List<ShareTarget>
        get() = runCatching {
            val a = JSONArray(p.getString("targets", "[]"))
            List(a.length()) { ShareTarget.fromJson(a.getJSONObject(it)) }
        }.getOrDefault(emptyList())
        set(v) = p.edit().putString("targets", JSONArray(v.map { it.toJson() }).toString()).apply()

    /** The IP last sent to, listed first. */
    var last: String?
        get() = p.getString("last", null)
        set(v) = p.edit().putString("last", v).apply()
}

/** Sends files and text to other devices, and finds the ones that receive. */
object ShareClient {
    /** What this phone calls itself to receivers ("Galaxy Z Fold8"). */
    fun deviceName(context: Context): String =
        Settings.Global.getString(context.contentResolver, Settings.Global.DEVICE_NAME)?.ifBlank { null } ?: Build.MODEL

    /** GET /share/info: the target, or null if nothing receives there. Blocking. */
    fun info(ip: String, timeoutMs: Int = 2000): ShareTarget? = try {
        val conn = URL("http://$ip:${ShareServer.PORT}/share/info").openConnection() as HttpURLConnection
        conn.connectTimeout = timeoutMs
        conn.readTimeout = timeoutMs
        try {
            if (conn.responseCode != 200) null
            else JSONObject(conn.inputStream.bufferedReader().readText()).let {
                ShareTarget(it.optString("name").ifEmpty { ip }, it.optString("os"), ip)
            }
        } finally {
            conn.disconnect()
        }
    } catch (e: Exception) {
        null
    }

    /**
     * Finds the devices that receive shares: those answering on the same
     * Wi-Fi, and since Android apps cannot read Tailscale's device list, it
     * also asks every Remoter it knows (saved computers and earlier targets)
     * for GET /devices, then each online device for /share/info. Blocking.
     */
    fun discover(prefs: Prefs, share: SharePrefs): List<ShareTarget> {
        val pool = Executors.newCachedThreadPool()
        try {
            val onWifi = pool.submit(Callable { Lan.discover() })
            val known = (prefs.computers.map { it.substringBefore(':') } + share.targets.map { it.ip }).distinct()
            val candidates = LinkedHashSet<String>(known)
            val lists = pool.invokeAll(known.map { ip -> Callable { devices(ip) } }, 10, TimeUnit.SECONDS)
            for (f in lists) {
                runCatching { f.get() }.getOrNull()?.let { candidates.addAll(it) }
            }
            // Devices on the same Wi-Fi are reached there, not over Tailscale.
            val home = runCatching { onWifi.get() }.getOrDefault(emptyList())
                .filter { it.share && it.ip !in Lan.ownIps() }
            candidates.removeAll(ownAddresses())
            candidates.removeAll(home.map { it.tailnet }.toSet())
            candidates.removeAll(home.map { it.ip }.toSet())
            val found = pool.invokeAll(candidates.map { ip -> Callable { info(ip) } }, 6, TimeUnit.SECONDS)
            return home.map { ShareTarget(it.name, it.os, it.ip) } + found.mapNotNull { runCatching { it.get() }.getOrNull() }
        } finally {
            pool.shutdownNow()
        }
    }

    /** The online tailnet IPs a Remoter at [ip] lists, or null. */
    private fun devices(ip: String): List<String>? = try {
        val conn = URL("http://$ip:${ShareServer.PORT}/devices").openConnection() as HttpURLConnection
        conn.connectTimeout = 2500
        conn.readTimeout = 8000
        try {
            if (conn.responseCode != 200) null
            else {
                val a: JSONArray = JSONObject(conn.inputStream.bufferedReader().readText()).optJSONArray("devices") ?: JSONArray()
                (0 until a.length()).map { a.getJSONObject(it) }
                    .filter { it.optBoolean("online") && it.optString("ip").isNotEmpty() }
                    .map { it.getString("ip") }
            }
        } finally {
            conn.disconnect()
        }
    } catch (e: Exception) {
        null
    }

    /** This phone's own Tailscale addresses, so it does not list itself. */
    fun ownAddresses(): Set<String> = try {
        NetworkInterface.getNetworkInterfaces().toList().flatMap { it.inetAddresses.toList() }
            .filterIsInstance<Inet4Address>().map { it.hostAddress ?: "" }.filter { it.startsWith("100.") }.toSet()
    } catch (e: Exception) {
        emptySet()
    }

    class Item(val uri: Uri, val name: String, val size: Long)

    fun describe(context: Context, uri: Uri): Item {
        var name: String? = null
        var size = -1L
        runCatching {
            context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
                if (c.moveToFirst()) {
                    val n = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    val s = c.getColumnIndex(OpenableColumns.SIZE)
                    if (n >= 0) name = c.getString(n)
                    if (s >= 0 && !c.isNull(s)) size = c.getLong(s)
                }
            }
        }
        return Item(uri, name ?: uri.lastPathSegment?.substringAfterLast('/') ?: "shared file", size)
    }

    /** POST /share/file. [progress] gets bytes sent so far. Blocking. */
    fun sendFile(context: Context, to: ShareTarget, item: Item, index: Int, count: Int, progress: (Long) -> Unit) {
        val input: InputStream = context.contentResolver.openInputStream(item.uri) ?: throw IOException("cannot read ${item.name}")
        input.use {
            val url = "${to.base}/share/file?name=" + URLEncoder.encode(item.name, "UTF-8").replace("+", "%20")
            val conn = URL(url).openConnection() as HttpURLConnection
            conn.requestMethod = "POST"
            conn.doOutput = true
            conn.connectTimeout = 8000
            conn.readTimeout = 60000
            if (item.size >= 0) conn.setFixedLengthStreamingMode(item.size) else conn.setChunkedStreamingMode(64 * 1024)
            conn.setRequestProperty("Content-Type", "application/octet-stream")
            conn.setRequestProperty("X-Remoter-From", deviceName(context))
            conn.setRequestProperty("X-Remoter-Index", index.toString())
            conn.setRequestProperty("X-Remoter-Count", count.toString())
            try {
                conn.outputStream.use { out ->
                    val buf = ByteArray(64 * 1024)
                    var sent = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        sent += n
                        progress(sent)
                    }
                }
                check(conn)
            } finally {
                conn.disconnect()
            }
        }
    }

    /** POST /share/text. Blocking. */
    fun sendText(context: Context, to: ShareTarget, text: String) {
        val conn = URL("${to.base}/share/text").openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 8000
        conn.readTimeout = 15000
        conn.setRequestProperty("Content-Type", "text/plain; charset=utf-8")
        conn.setRequestProperty("X-Remoter-From", deviceName(context))
        try {
            conn.outputStream.use { it.write(text.toByteArray()) }
            check(conn)
        } finally {
            conn.disconnect()
        }
    }

    private fun check(conn: HttpURLConnection) {
        val code = conn.responseCode
        if (code / 100 != 2) {
            val msg = runCatching { conn.errorStream?.bufferedReader()?.readText() }.getOrNull()?.trim()
            throw IOException(if (msg.isNullOrEmpty()) "HTTP $code" else msg)
        }
    }
}
