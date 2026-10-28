package io.github.aleynikovich.remoter

import android.app.DownloadManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.ClipboardManager
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.Uri
import android.os.Environment
import android.os.IBinder
import android.os.SystemClock
import android.provider.MediaStore
import android.util.Log
import android.util.Size
import android.webkit.MimeTypeMap
import org.json.JSONObject
import java.io.InputStream
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/**
 * Remoter's file sharing in the background: listens for files and links from
 * the PCs ([ShareServer]) and sends what [ShareActivity] hands it, so a
 * transfer carries on after the share sheet closes. A foreground service
 * with a quiet ongoing notification, so Android keeps it alive.
 */
class ShareService : Service() {
    private lateinit var server: ShareServer
    private lateinit var share: SharePrefs
    private val senders = Executors.newSingleThreadExecutor()
    private val sending = AtomicInteger()
    private val ids = AtomicInteger(100)
    private val nm by lazy { getSystemService(NotificationManager::class.java) }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        share = SharePrefs(this)
        channels()
        server = ShareServer(
            info = {
                JSONObject().put("name", ShareClient.deviceName(this)).put("os", "android").put("version", BuildConfig.VERSION_NAME)
            },
            receiver = object : ShareServer.Receiver {
                override fun saveFile(from: String, name: String, size: Long, body: InputStream) = save(name, body)
                override fun filesDone(from: String, saved: List<String>) = received(from, saved.map(Uri::parse))
                override fun text(from: String, text: String) = receivedText(from, text)
            },
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(ONGOING_ID, ongoing(), ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        when (intent?.action) {
            ACTION_STOP -> {
                share.receive = false
                server.stop()
                if (sending.get() == 0) stopSelf()
                return START_NOT_STICKY
            }
            ACTION_SEND -> send(intent)
        }
        if (share.receive) {
            if (!server.start()) Log.w(TAG, server.error ?: "cannot listen")
            nm.notify(ONGOING_ID, ongoing())
        } else if (sending.get() == 0 && intent?.action != ACTION_SEND) {
            stopSelf()
        }
        return if (share.receive) START_STICKY else START_NOT_STICKY
    }

    override fun onDestroy() {
        server.stop()
        senders.shutdownNow()
        super.onDestroy()
    }

    private fun channels() {
        nm.createNotificationChannel(NotificationChannel(CH_ONGOING, "Ready to receive", NotificationManager.IMPORTANCE_MIN).apply {
            description = "Shown while Remoter can receive files from your PCs"
        })
        nm.createNotificationChannel(NotificationChannel(CH_RECEIVED, "Received files and links", NotificationManager.IMPORTANCE_HIGH))
        nm.createNotificationChannel(NotificationChannel(CH_SENDING, "Sending", NotificationManager.IMPORTANCE_LOW))
    }

    private fun ongoing(): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, ConnectActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, ShareService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE)
        val text = when {
            !share.receive -> "Sending…"
            server.error != null -> server.error
            else -> "Your PCs can send files and links to this phone"
        }
        return Notification.Builder(this, CH_ONGOING)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle("Remoter")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setShowWhen(false)
            .apply { if (share.receive) addAction(Notification.Action.Builder(null, "Stop receiving", stop).build()) }
            .build()
    }

    // ---- Receiving ----

    /** Writes a file into Downloads; returns its content URI. */
    private fun save(name: String, body: InputStream): String {
        val ext = name.substringAfterLast('.', "").lowercase()
        val mime = MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
        val values = ContentValues().apply {
            put(MediaStore.Downloads.DISPLAY_NAME, name)
            put(MediaStore.Downloads.MIME_TYPE, mime)
            put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
            put(MediaStore.Downloads.IS_PENDING, 1)
        }
        val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: throw java.io.IOException("cannot create $name in Downloads")
        try {
            contentResolver.openOutputStream(uri)!!.use { out -> body.copyTo(out, 64 * 1024) }
            contentResolver.update(uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null)
        } catch (e: Exception) {
            contentResolver.delete(uri, null, null)
            throw e
        }
        Log.i(TAG, "received $name -> $uri")
        return uri.toString()
    }

    private fun received(from: String, uris: List<Uri>) {
        if (uris.isEmpty()) return
        val b = Notification.Builder(this, CH_RECEIVED)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setAutoCancel(true)
        if (uris.size == 1) {
            val uri = uris[0]
            val mime = contentResolver.getType(uri) ?: "application/octet-stream"
            val name = ShareClient.describe(this, uri).name
            val kind = when {
                mime.startsWith("image/") -> "Photo"
                mime.startsWith("video/") -> "Video"
                mime == "application/pdf" -> "PDF"
                else -> "File"
            }
            val view = Intent(Intent.ACTION_VIEW).setDataAndType(uri, mime).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
            val send = Intent.createChooser(
                Intent(Intent.ACTION_SEND).setType(mime).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION),
                name,
            ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            val id = ids.incrementAndGet()
            b.setContentTitle("$kind from $from")
                .setContentText("$name · in Downloads")
                .setContentIntent(PendingIntent.getActivity(this, id * 2, view, PendingIntent.FLAG_IMMUTABLE))
                .addAction(Notification.Action.Builder(null, "Share", PendingIntent.getActivity(this, id * 2 + 1, send, PendingIntent.FLAG_IMMUTABLE)).build())
            if (mime.startsWith("image/")) {
                runCatching { contentResolver.loadThumbnail(uri, Size(1024, 1024), null) }.getOrNull()?.let { bmp ->
                    b.setLargeIcon(bmp).setStyle(Notification.BigPictureStyle().bigPicture(bmp))
                }
            }
            nm.notify(id, b.build())
        } else {
            val downloads = Intent(DownloadManager.ACTION_VIEW_DOWNLOADS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            val names = uris.take(3).joinToString(", ") { ShareClient.describe(this, it).name } +
                if (uris.size > 3) " and ${uris.size - 3} more" else ""
            val id = ids.incrementAndGet()
            b.setContentTitle("${uris.size} files from $from")
                .setContentText("$names · in Downloads")
                .setStyle(Notification.BigTextStyle().bigText("$names\nSaved in Downloads"))
                .setContentIntent(PendingIntent.getActivity(this, id * 2, downloads, PendingIntent.FLAG_IMMUTABLE))
            nm.notify(id, b.build())
        }
    }

    private fun receivedText(from: String, text: String) {
        getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("From $from", text))
        val link = !text.contains(Regex("\\s")) && (text.startsWith("http://", true) || text.startsWith("https://", true))
        val id = ids.incrementAndGet()
        val b = Notification.Builder(this, CH_RECEIVED)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setAutoCancel(true)
            .setContentTitle(if (link) "Link from $from" else "Copied text from $from")
            .setContentText(text.take(300))
            .setStyle(Notification.BigTextStyle().bigText(text.take(1000)))
        if (link) {
            val open = Intent(Intent.ACTION_VIEW, Uri.parse(text)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            b.setContentIntent(PendingIntent.getActivity(this, id * 2, open, PendingIntent.FLAG_IMMUTABLE))
        }
        nm.notify(id, b.build())
    }

    // ---- Sending ----

    private fun send(intent: Intent) {
        val target = runCatching { ShareTarget.fromJson(JSONObject(intent.getStringExtra(EXTRA_TARGET)!!)) }.getOrNull() ?: return
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)
        val clip = intent.clipData
        val uris = if (clip == null) emptyList() else (0 until clip.itemCount).mapNotNull { clip.getItemAt(it).uri }
        if (uris.isEmpty() && text.isNullOrBlank()) return
        sending.incrementAndGet()
        nm.notify(ONGOING_ID, ongoing())
        senders.execute {
            val id = ids.incrementAndGet()
            val b = Notification.Builder(this, CH_SENDING)
                .setSmallIcon(android.R.drawable.stat_sys_upload)
                .setOnlyAlertOnce(true)
                .setOngoing(true)
            try {
                if (uris.isEmpty()) {
                    ShareClient.sendText(this, target, text!!)
                    finished(id, "Sent to ${target.name}", text.take(200))
                } else {
                    val items = uris.map { ShareClient.describe(this, it) }
                    val total = items.sumOf { maxOf(it.size, 0) }
                    var before = 0L
                    var shown = 0L
                    items.forEachIndexed { i, item ->
                        val title = if (items.size == 1) "Sending ${item.name} to ${target.name}" else "Sending ${i + 1} of ${items.size} to ${target.name}"
                        b.setContentTitle(title).setContentText(item.name).setProgress(100, 0, total <= 0)
                        nm.notify(id, b.build())
                        ShareClient.sendFile(this, target, item, i + 1, items.size) { sent ->
                            val now = SystemClock.uptimeMillis()
                            if (total > 0 && now - shown > 300) {
                                shown = now
                                nm.notify(id, b.setProgress(100, ((before + sent) * 100 / total).toInt(), false).build())
                            }
                        }
                        before += maxOf(item.size, 0)
                    }
                    finished(id, "Sent to ${target.name}", if (items.size == 1) items[0].name else "${items.size} files")
                }
                share.last = target.ip
            } catch (e: Exception) {
                Log.w(TAG, "sending to ${target.ip} failed", e)
                nm.cancel(id)
                nm.notify(ids.incrementAndGet(), Notification.Builder(this, CH_RECEIVED)
                    .setSmallIcon(android.R.drawable.stat_notify_error)
                    .setAutoCancel(true)
                    .setContentTitle("Could not send to ${target.name}")
                    .setContentText(e.message ?: e.toString())
                    .build())
            } finally {
                if (sending.decrementAndGet() == 0 && !share.receive) stopSelf()
            }
        }
    }

    private fun finished(id: Int, title: String, text: String) {
        nm.notify(id, Notification.Builder(this, CH_SENDING)
            .setSmallIcon(android.R.drawable.stat_sys_upload_done)
            .setContentTitle(title)
            .setContentText(text)
            .setAutoCancel(true)
            .setTimeoutAfter(6000)
            .build())
    }

    companion object {
        private const val TAG = "ShareService"
        private const val ONGOING_ID = 1
        private const val CH_ONGOING = "share-ongoing"
        private const val CH_RECEIVED = "share-received"
        private const val CH_SENDING = "share-sending"
        const val ACTION_SEND = "io.github.aleynikovich.remoter.SHARE_SEND"
        const val ACTION_STOP = "io.github.aleynikovich.remoter.SHARE_STOP"
        const val EXTRA_TARGET = "target"

        /** Starts receiving if it is switched on. Safe to call any time. */
        fun start(context: Context) {
            if (!SharePrefs(context).receive) return
            try {
                context.startForegroundService(Intent(context, ShareService::class.java))
            } catch (e: IllegalStateException) {
                // ForegroundServiceStartNotAllowedException: not from the background.
                Log.w(TAG, "cannot start receiving now", e)
            }
        }
    }
}

/** Starts receiving after a reboot or an app update. */
class ShareBootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED -> ShareService.start(context)
        }
    }
}
