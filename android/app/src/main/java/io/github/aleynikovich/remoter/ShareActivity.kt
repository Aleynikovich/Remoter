package io.github.aleynikovich.remoter

import android.app.Activity
import android.content.ClipData
import android.content.Intent
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.os.Bundle
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

/**
 * "Share → Remoter" in Android's share sheet: pick the PC to send the
 * shared files, photos or link to. The sending itself runs in
 * [ShareService], so this closes at once.
 */
class ShareActivity : Activity() {
    private lateinit var share: SharePrefs
    private lateinit var list: LinearLayout
    private lateinit var status: TextView
    private val pool = Executors.newSingleThreadExecutor()
    private var uris: List<Uri> = emptyList()
    private var text: String? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        share = SharePrefs(this)
        readIntent()
        if (uris.isEmpty() && text.isNullOrBlank()) {
            Toast.makeText(this, "Nothing to send", Toast.LENGTH_SHORT).show()
            finish()
            return
        }
        buildViews()
        show(share.targets, searching = true)
        pool.execute {
            val found = ShareClient.discover(Prefs(this), share)
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                if (found.isNotEmpty()) share.targets = found
                show(found, searching = false)
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        pool.shutdownNow()
    }

    @Suppress("DEPRECATION")
    private fun readIntent() {
        val i = intent
        uris = when (i.action) {
            Intent.ACTION_SEND -> listOfNotNull(i.getParcelableExtra(Intent.EXTRA_STREAM))
            Intent.ACTION_SEND_MULTIPLE -> i.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM).orEmpty()
            else -> emptyList()
        }
        if (uris.isEmpty()) text = i.getStringExtra(Intent.EXTRA_TEXT) ?: i.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
    }

    private fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), resources.displayMetrics).toInt()

    private fun label(size: Float, color: Int, s: String = "") = TextView(this).apply {
        textSize = size
        setTextColor(color)
        text = s
    }

    private fun buildViews() {
        val what = when {
            uris.size == 1 -> ShareClient.describe(this, uris[0]).name
            uris.size > 1 -> "${uris.size} files"
            else -> text!!.trim().let { if (it.length > 60) it.take(60) + "…" else it }
        }
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(20), dp(20), dp(12))
        }
        column.addView(label(20f, Color.WHITE, "Send to"))
        column.addView(label(13f, DIM, what).apply {
            maxLines = 2
            setPadding(0, dp(2), 0, dp(14))
        })
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        column.addView(list)
        status = label(13f, DIM).apply { setPadding(0, dp(8), 0, dp(8)) }
        column.addView(status)
        column.addView(label(15f, 0xFF8AB4FF.toInt(), "Cancel").apply {
            gravity = Gravity.END
            setPadding(dp(8), dp(8), dp(4), dp(8))
            setOnClickListener { finish() }
        })
        setContentView(column, ViewGroup.LayoutParams(dp(360).coerceAtMost(resources.displayMetrics.widthPixels - dp(32)), ViewGroup.LayoutParams.WRAP_CONTENT))
    }

    private fun show(targets: List<ShareTarget>, searching: Boolean) {
        list.removeAllViews()
        val last = share.last
        val sorted = targets.sortedWith(compareBy<ShareTarget>({ it.ip != last }, { it.name.lowercase() }))
        for (t in sorted) list.addView(row(t, online = !searching), LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            bottomMargin = dp(8)
        })
        status.text = when {
            searching -> "Looking for your devices…"
            targets.isEmpty() -> "No PC is receiving. Run Remoter there (on Linux: remoter -mode share) and make sure Tailscale is on."
            else -> ""
        }
    }

    private fun row(t: ShareTarget, online: Boolean): View {
        val dot = View(this).apply {
            background = GradientDrawable().apply {
                shape = GradientDrawable.OVAL
                setColor(if (online) GREEN else GREY)
            }
        }
        return LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(14), dp(14), dp(14), dp(14))
            setBackgroundResource(R.drawable.key_bg)
            addView(dot, LinearLayout.LayoutParams(dp(10), dp(10)).apply { marginEnd = dp(14) })
            addView(LinearLayout(this@ShareActivity).apply {
                orientation = LinearLayout.VERTICAL
                addView(label(17f, Color.WHITE, t.name))
                addView(label(13f, DIM, listOf(osName(t.os), t.ip).filter { it.isNotEmpty() }.joinToString(" · ")))
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            setOnClickListener { send(t) }
        }
    }

    private fun osName(os: String) = when (os.lowercase()) {
        "linux" -> "Linux"
        "windows" -> "Windows"
        "darwin", "macos" -> "Mac"
        "android" -> "Android"
        else -> os
    }

    private fun send(t: ShareTarget) {
        val i = Intent(this, ShareService::class.java)
            .setAction(ShareService.ACTION_SEND)
            .putExtra(ShareService.EXTRA_TARGET, t.toJson().toString())
        if (uris.isNotEmpty()) {
            // Passing the files on with read permission lets the service
            // keep reading them after this screen is gone.
            val clip = ClipData.newRawUri("files", uris[0])
            for (u in uris.drop(1)) clip.addItem(ClipData.Item(u))
            i.clipData = clip
            i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        } else {
            i.putExtra(Intent.EXTRA_TEXT, text)
        }
        startForegroundService(i)
        Toast.makeText(this, "Sending to ${t.name}…", Toast.LENGTH_SHORT).show()
        finish()
    }

    companion object {
        private const val DIM = 0xFF9AA3B2.toInt()
        private const val GREEN = 0xFF34D399.toInt()
        private const val GREY = 0xFF6B7280.toInt()
    }
}
