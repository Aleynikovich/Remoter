package io.github.aleynikovich.remoter

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.util.Log
import android.widget.Toast
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

/**
 * Finds and installs newer builds of this app. CI publishes every build to the
 * GitHub prerelease tagged [TAG] (one rolling release, apart from the Windows
 * host's dev-N releases so the host's own updater never sees it) with the build
 * number in its title.
 */
object Updater {
    const val TAG = "android-latest"
    const val ASSET = "remoter-android.apk"
    private const val API = "https://api.github.com/repos/aleynikovich/remoter/releases/tags/$TAG"
    const val DOWNLOAD = "https://github.com/aleynikovich/remoter/releases/download/$TAG/$ASSET"

    class Update(val build: Int, val name: String, val url: String, val size: Long)

    /** Returns a newer build than [current], or null. Blocking; call off the main thread. */
    fun check(current: Int): Update? {
        val conn = URL(API).openConnection() as HttpURLConnection
        conn.connectTimeout = 8000
        conn.readTimeout = 8000
        conn.setRequestProperty("Accept", "application/vnd.github+json")
        try {
            if (conn.responseCode != 200) return null
            val release = JSONObject(conn.inputStream.bufferedReader().readText())
            val name = release.optString("name")
            val build = Regex("android-dev-(\\d+)").find(name)?.groupValues?.get(1)?.toIntOrNull() ?: return null
            val assets = release.optJSONArray("assets") ?: return null
            for (i in 0 until assets.length()) {
                val a = assets.getJSONObject(i)
                if (a.optString("name") == ASSET && build > current) {
                    return Update(build, "android-dev-$build", a.optString("browser_download_url"), a.optLong("size"))
                }
            }
            return null
        } finally {
            conn.disconnect()
        }
    }

    /**
     * Downloads the APK into a package installer session and commits it; the
     * system then asks to confirm the update. Blocking; call off the main thread.
     */
    fun install(context: Context, update: Update, progress: (Int) -> Unit) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(context.packageName)
        if (update.size > 0) params.setSize(update.size)
        val id = installer.createSession(params)
        val session = installer.openSession(id)
        try {
            val conn = URL(update.url).openConnection() as HttpURLConnection
            conn.connectTimeout = 10000
            conn.readTimeout = 20000
            conn.instanceFollowRedirects = true
            val total = conn.contentLengthLong.takeIf { it > 0 } ?: update.size
            conn.inputStream.use { input ->
                session.openWrite(ASSET, 0, total).use { out ->
                    val buf = ByteArray(64 * 1024)
                    var done = 0L
                    var last = -1
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        done += n
                        val pct = if (total > 0) (done * 100 / total).toInt() else 0
                        if (pct != last) {
                            last = pct
                            progress(pct)
                        }
                    }
                    session.fsync(out)
                }
            }
            val intent = Intent(context, InstallReceiver::class.java)
            val pending = PendingIntent.getBroadcast(
                context, id, intent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
            )
            session.commit(pending.intentSender)
        } catch (e: Exception) {
            session.abandon()
            throw e
        } finally {
            session.close()
        }
    }
}

/** Receives the installer's answer: shows its confirmation screen, or the error. */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT) ?: return
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                context.startActivity(confirm)
            }
            PackageInstaller.STATUS_SUCCESS -> {}
            else -> {
                val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "status $status"
                Log.w("Updater", "install failed: $msg")
                Toast.makeText(context, "Update failed: $msg", Toast.LENGTH_LONG).show()
            }
        }
    }
}
