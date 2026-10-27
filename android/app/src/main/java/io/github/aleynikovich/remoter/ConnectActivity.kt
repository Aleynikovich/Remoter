package io.github.aleynikovich.remoter

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors

/**
 * The start screen: the saved computers, each one tap away, with whether
 * a Remoter is answering there right now.
 */
class ConnectActivity : Activity() {
    private lateinit var prefs: Prefs
    private lateinit var list: LinearLayout
    private lateinit var status: TextView
    private lateinit var updateBanner: TextView
    private val main = Handler(Looper.getMainLooper())
    private val pool = Executors.newCachedThreadPool()
    private var update: Updater.Update? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        buildViews()
        checkForUpdate()
    }

    override fun onResume() {
        super.onResume()
        showComputers()
    }

    override fun onDestroy() {
        super.onDestroy()
        pool.shutdownNow()
    }

    private fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), resources.displayMetrics).toInt()

    private fun text(size: Float, color: Int, s: String = "") = TextView(this).apply {
        textSize = size
        setTextColor(color)
        text = s
    }

    private fun buildViews() {
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(24), dp(32), dp(24), dp(24))
        }
        column.addView(text(28f, Color.WHITE, "Remoter"))
        column.addView(text(13f, 0xFF9AA3B2.toInt(), "Android viewer · ${BuildConfig.VERSION_NAME}").apply {
            setPadding(0, dp(2), 0, dp(20))
        })

        updateBanner = text(15f, Color.WHITE).apply {
            setPadding(dp(14), dp(12), dp(14), dp(12))
            setBackgroundResource(R.drawable.primary_bg)
            visibility = View.GONE
            setOnClickListener { installUpdate() }
        }
        column.addView(updateBanner, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            bottomMargin = dp(16)
        })

        column.addView(text(13f, 0xFF9AA3B2.toInt(), "COMPUTERS").apply { setPadding(0, 0, 0, dp(8)) })
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        column.addView(list)

        val add = EditText(this).apply {
            hint = "Add a computer: Tailscale IP or name"
            setHintTextColor(0xFF6B7280.toInt())
            setTextColor(Color.WHITE)
            isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            imeOptions = EditorInfo.IME_ACTION_DONE
            setBackgroundResource(R.drawable.field_bg)
            setPadding(dp(12), dp(12), dp(12), dp(12))
        }
        val addRow = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(8), 0, 0)
            addView(add, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(text(15f, Color.WHITE, "Add").apply {
                gravity = Gravity.CENTER
                setPadding(dp(18), dp(12), dp(18), dp(12))
                setBackgroundResource(R.drawable.primary_bg)
                setOnClickListener {
                    val v = add.text.toString().trim()
                    if (v.isNotEmpty()) {
                        prefs.used(v)
                        add.setText("")
                        showComputers()
                    }
                }
            }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                marginStart = dp(8)
            })
        }
        column.addView(addRow)

        status = text(14f, 0xFFFF9B9B.toInt()).apply { setPadding(0, dp(16), 0, 0) }
        column.addView(status)

        column.addView(CheckBox(this).apply {
            text = "H.264 video (hardware decoding; off = JPEG tiles)"
            setTextColor(0xFFD0D4DC.toInt())
            isChecked = prefs.video
            setOnCheckedChangeListener { _, on -> prefs.video = on }
            setPadding(dp(4), dp(20), 0, 0)
        })
        column.addView(text(13f, 0xFF9AA3B2.toInt(), HELP).apply { setPadding(0, dp(16), 0, 0) })

        // Centered and no wider than a phone, also on the unfolded screen.
        val frame = LinearLayout(this).apply {
            gravity = Gravity.CENTER_HORIZONTAL
            addView(column, LinearLayout.LayoutParams(dp(520).coerceAtMost(resources.displayMetrics.widthPixels), ViewGroup.LayoutParams.WRAP_CONTENT))
        }
        val scroll = ScrollView(this).apply {
            isFillViewport = true
            addView(frame)
        }
        scroll.setOnApplyWindowInsetsListener { v, insets ->
            val bars = insets.getInsets(android.view.WindowInsets.Type.systemBars() or android.view.WindowInsets.Type.ime())
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }
        setContentView(scroll)
    }

    /** One row of the list: a dot, a name and a line of detail. */
    private class Row(val view: View, val dot: View, val title: TextView, val detail: TextView)

    private fun row(title: String, detail: String, onClick: () -> Unit, onLongClick: (() -> Unit)?): Row {
        val dot = View(this).apply {
            background = GradientDrawable().apply {
                shape = GradientDrawable.OVAL
                setColor(GREY)
            }
        }
        val titleView = text(17f, Color.WHITE, title)
        val detailView = text(13f, 0xFF9AA3B2.toInt(), detail)
        val view = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(14), dp(14), dp(14), dp(14))
            setBackgroundResource(R.drawable.key_bg)
            addView(dot, LinearLayout.LayoutParams(dp(10), dp(10)).apply { marginEnd = dp(14) })
            addView(LinearLayout(this@ConnectActivity).apply {
                orientation = LinearLayout.VERTICAL
                addView(titleView)
                addView(detailView)
            }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
            addView(text(15f, 0xFF8AB4FF.toInt(), "Connect"))
            setOnClickListener { onClick() }
            if (onLongClick != null) setOnLongClickListener {
                onLongClick()
                true
            }
        }
        return Row(view, dot, titleView, detailView)
    }

    private fun Row.state(online: Boolean) = (dot.background as GradientDrawable).setColor(if (online) GREEN else GREY)

    private fun addRow(r: Row) = list.addView(r.view, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
        bottomMargin = dp(8)
    })

    private val tailnetHeader by lazy {
        text(13f, 0xFF9AA3B2.toInt(), "ON YOUR TAILNET").apply { setPadding(0, dp(12), 0, dp(8)) }
    }
    private val tailnetRows = mutableMapOf<String, Row>()
    private val savedRows = mutableMapOf<String, Row>()

    /**
     * Saved computers first, then the other devices on the tailnet. Android
     * apps cannot ask Tailscale for its device list, so it comes from the
     * Remoter hosts themselves (GET /devices on each one that answers).
     */
    private fun showComputers() {
        list.removeAllViews()
        tailnetRows.clear()
        savedRows.clear()
        for (c in prefs.computers) {
            val r = row(prefs.name(c) ?: c, "Checking…", { connect(c) }, { computerMenu(c) })
            if (prefs.name(c) != null) r.detail.text = "$c · checking…"
            savedRows[c] = r
            addRow(r)
            probe(c) { build ->
                r.state(build != null)
                val where = if (prefs.name(c) != null) "$c · " else ""
                r.detail.text = where + if (build != null) "Remoter $build" else "Not answering"
                if (build != null) discover(c)
            }
        }
        list.addView(tailnetHeader)
        tailnetHeader.visibility = View.GONE
        prefs.tailnet?.let { cached -> runCatching { showTailnet(JSONObject(cached), fresh = false) } }
    }

    /** Asks a host for the devices on its tailnet. Hosts without /devices just say 404. */
    private fun discover(computer: String) {
        pool.execute {
            val json = try {
                val conn = URL(RelayClient.baseUrl(computer) + "/devices").openConnection() as HttpURLConnection
                conn.connectTimeout = 2500
                conn.readTimeout = 8000
                try {
                    if (conn.responseCode == 200) JSONObject(conn.inputStream.bufferedReader().readText()) else null
                } finally {
                    conn.disconnect()
                }
            } catch (e: Exception) {
                null
            } ?: return@execute
            main.post {
                if (isDestroyed) return@post
                prefs.tailnet = json.toString()
                showTailnet(json, fresh = true)
            }
        }
    }

    private fun showTailnet(json: JSONObject, fresh: Boolean) {
        val devices = json.optJSONArray("devices") ?: return
        for (i in 0 until devices.length()) {
            val d = devices.getJSONObject(i)
            val ip = d.optString("ip")
            if (ip.isEmpty() || d.optString("os").equals("android", true) || d.optString("os").equals("iOS", true)) continue
            val addr = "$ip:8080"
            val name = d.optString("name").ifEmpty { ip }
            val remoter = d.optString("remoter")
            val online = d.optBoolean("online")
            if (prefs.name(addr) != name) prefs.setName(addr, name)
            val detail = "$ip · " + when {
                remoter.isNotEmpty() -> "Remoter $remoter"
                online -> "online, Remoter not running"
                else -> "offline"
            } + if (fresh) "" else " (last seen)"
            val saved = savedRows[addr]
            if (saved != null) {
                saved.title.text = name
                continue
            }
            val r = tailnetRows.getOrPut(addr) {
                row(name, detail, { connect(addr) }, null).also {
                    list.addView(it.view, list.childCount, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                        bottomMargin = dp(8)
                    })
                }
            }
            r.title.text = name
            r.detail.text = detail
            r.state(remoter.isNotEmpty())
            tailnetHeader.visibility = View.VISIBLE
        }
    }

    /** GET /version: a Remoter relay answers with its build tag. */
    private fun probe(computer: String, done: (String?) -> Unit) {
        pool.execute {
            val build = try {
                val conn = URL(RelayClient.baseUrl(computer) + "/version").openConnection() as HttpURLConnection
                conn.connectTimeout = 2500
                conn.readTimeout = 2500
                try {
                    if (conn.responseCode == 200) conn.inputStream.bufferedReader().readText().trim().ifEmpty { "Remoter" } else null
                } finally {
                    conn.disconnect()
                }
            } catch (e: Exception) {
                null
            }
            main.post { if (!isDestroyed) done(build) }
        }
    }

    private fun computerMenu(c: String) {
        AlertDialog.Builder(this)
            .setTitle(c)
            .setItems(arrayOf("Connect", "Set password", "Remove")) { _, which ->
                when (which) {
                    0 -> connect(c)
                    1 -> askPassword(c, null)
                    2 -> {
                        prefs.computers = prefs.computers.filter { it != c }
                        showComputers()
                    }
                }
            }
            .show()
    }

    private fun askPassword(c: String, message: String?) {
        val field = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            setText(prefs.password(c))
            selectAll()
        }
        val box = LinearLayout(this).apply {
            setPadding(dp(20), dp(8), dp(20), 0)
            addView(field, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
        AlertDialog.Builder(this)
            .setTitle("Password for $c")
            .setMessage(message)
            .setView(box)
            .setPositiveButton("Connect") { _, _ ->
                prefs.setPassword(c, field.text.toString())
                connect(c)
            }
            .setNegativeButton("Cancel", null)
            .show()
        field.requestFocus()
    }

    private fun connect(c: String) {
        status.text = ""
        prefs.used(c)
        @Suppress("DEPRECATION")
        startActivityForResult(
            Intent(this, SessionActivity::class.java)
                .putExtra(SessionActivity.EXTRA_COMPUTER, c)
                .putExtra(SessionActivity.EXTRA_PASSWORD, prefs.password(c)),
            REQUEST_SESSION,
        )
    }

    @Deprecated("Activity result of the session")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode != REQUEST_SESSION || data == null) return
        val c = data.getStringExtra(SessionActivity.EXTRA_COMPUTER) ?: return
        if (data.getBooleanExtra(SessionActivity.EXTRA_WRONG_PASSWORD, false)) {
            askPassword(c, "The computer did not accept the password.")
        } else {
            status.text = "$c: ${data.getStringExtra(SessionActivity.EXTRA_REASON)}"
        }
    }

    // ---- Updates ----

    private fun checkForUpdate() {
        if (BuildConfig.VERSION_CODE <= 0) return // local build
        pool.execute {
            val u = try {
                Updater.check(BuildConfig.VERSION_CODE)
            } catch (e: Exception) {
                null
            }
            main.post {
                update = u
                if (u != null && !isDestroyed) {
                    updateBanner.text = "Update available: ${u.name}. Tap to install."
                    updateBanner.visibility = View.VISIBLE
                }
            }
        }
    }

    private fun installUpdate() {
        val u = update ?: return
        if (!packageManager.canRequestPackageInstalls()) {
            updateBanner.text = "Allow Remoter to install apps, then tap again."
            startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
            return
        }
        updateBanner.isEnabled = false
        updateBanner.text = "Downloading ${u.name}…"
        pool.execute {
            try {
                Updater.install(this, u) { pct -> main.post { updateBanner.text = "Downloading ${u.name}… $pct%" } }
                main.post { updateBanner.text = "Installing ${u.name}…" }
            } catch (e: Exception) {
                main.post {
                    updateBanner.isEnabled = true
                    updateBanner.text = "Update failed (${e.message}). Tap to retry."
                }
            }
        }
    }

    companion object {
        private const val REQUEST_SESSION = 1
        private const val GREEN = 0xFF34D399.toInt()
        private const val GREY = 0xFF6B7280.toInt()
        private const val HELP = "Tap a computer to connect. The password defaults to root.\n\n" +
            "Touch mouse (default): slide to move the pointer, tap to click, long-press or tap-and-slide to drag.\n" +
            "Direct touch: tap to click where you touch, long-press for right click.\n" +
            "Both: pinch to zoom, two fingers to scroll, two-finger tap for right click, " +
            "three fingers to pan, three-finger tap for the keyboard.\n\n" +
            "With a mouse (DeX or Bluetooth), Lock mouse captures it; Ctrl+Alt+Esc lets go."
    }
}
