package io.github.aleynikovich.remoter

import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.res.Configuration
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.Gravity
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.Surface
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.widget.FrameLayout
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

/** A remote control session: the remote screen, a toolbar and the special-keys bar. */
class SessionActivity : Activity(), RelayClient.Listener, ScreenView.Remote {

    private lateinit var prefs: Prefs
    private lateinit var computer: String
    private lateinit var password: String

    private lateinit var screen: ScreenView
    private lateinit var column: LinearLayout
    private lateinit var toolbar: View
    private lateinit var keysBar: View
    private lateinit var handle: TextView
    private lateinit var notice: TextView
    private lateinit var stats: TextView
    private lateinit var modeButton: TextView
    private lateinit var captureButton: TextView
    private lateinit var displayButton: TextView
    private lateinit var keyboardButton: TextView
    private val modifierViews = mutableMapOf<String, TextView>()

    private var client: RelayClient? = null
    private lateinit var decoder: VideoDecoder
    private lateinit var keyboard: RemoteKeyboard
    private val main = Handler(Looper.getMainLooper())

    private var useVideo = true
    private var decoderErrors = 0
    /** What the host is sending: null until the first update, then H.264 or not. */
    @Volatile private var videoMode: Boolean? = null
    @Volatile private var shownW = 0
    @Volatile private var shownH = 0
    private var surfaceReady = false
    private var imeVisible = false
    private var displayCount = 1
    private var display = 0
    private var lastClipboard: String? = null

    private val frames = AtomicInteger()
    private val bytes = AtomicLong()

    private lateinit var clipboard: ClipboardManager
    private val clipListener = ClipboardManager.OnPrimaryClipChangedListener {
        if (hasWindowFocus()) sendClipboard()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        computer = intent.getStringExtra(EXTRA_COMPUTER) ?: Prefs.DEFAULT_COMPUTER
        password = intent.getStringExtra(EXTRA_PASSWORD) ?: Prefs.DEFAULT_PASSWORD
        useVideo = prefs.video && VideoDecoder.supported
        clipboard = getSystemService(ClipboardManager::class.java)!!

        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        window.setDecorFitsSystemWindows(false)

        keyboard = RemoteKeyboard { kind, key, code ->
            client?.input(kind) {
                put("key", key)
                put("code", code)
            }
        }
        keyboard.onLatchChanged = ::showLatches
        keyboard.onReleaseCombo = {
            if (screen.isCaptured) screen.capture(false)
        }

        decoder = VideoDecoder(
            ack = { seq -> client?.ack(seq) },
            onError = { main.post(::videoFailed) },
            onFrameDrawn = { frames.incrementAndGet() },
        )

        buildViews()
        hideSystemBars()
        main.post(statsTick)
    }

    // ---- Views ----

    private fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), resources.displayMetrics).toInt()

    private fun button(label: String, onClick: () -> Unit): TextView = TextView(this).apply {
        text = label
        setTextColor(Color.WHITE)
        textSize = 14f
        gravity = Gravity.CENTER
        minWidth = dp(44)
        setPadding(dp(10), 0, dp(10), 0)
        setBackgroundResource(R.drawable.key_bg)
        isFocusable = false
        setOnClickListener { onClick() }
        layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, dp(38)).apply {
            marginEnd = dp(5)
        }
    }

    private fun bar(vararg views: View): HorizontalScrollView {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(6), dp(5), dp(1), dp(5))
        }
        views.forEach { row.addView(it) }
        return HorizontalScrollView(this).apply {
            isHorizontalScrollBarEnabled = false
            isFocusable = false
            setBackgroundResource(R.drawable.bar_bg)
            addView(row)
        }
    }

    private fun spacer() = View(this).apply { layoutParams = LinearLayout.LayoutParams(dp(10), 1) }

    @SuppressLint("ClickableViewAccessibility")
    private fun repeating(label: String, code: String): TextView = button(label) {}.apply {
        // Arrows and Backspace repeat while held, like a real keyboard.
        val repeat = object : Runnable {
            override fun run() {
                keyboard.tap(code)
                main.postDelayed(this, 60)
            }
        }
        setOnClickListener(null)
        setOnTouchListener { v, e ->
            when (e.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    v.isPressed = true
                    keyboard.tap(code)
                    main.postDelayed(repeat, 450)
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                    v.isPressed = false
                    main.removeCallbacks(repeat)
                }
            }
            true
        }
    }

    private fun modifier(label: String, code: String): TextView = button(label) { keyboard.tapModifier(code) }.apply {
        setOnLongClickListener {
            keyboard.lockModifier(code)
            true
        }
        modifierViews[code] = this
    }

    private fun buildViews() {
        screen = ScreenView(this, this)
        screen.mode = if (prefs.trackpad) ScreenView.Mode.TRACKPAD else ScreenView.Mode.TOUCH
        screen.setBackgroundColor(Color.BLACK)

        keyboardButton = button("⌨ Keyboard") { toggleKeyboard() }
        modeButton = button("") { toggleMode() }
        captureButton = button("Lock mouse") { screen.capture(!screen.isCaptured) }
        displayButton = button("Screen 1") { pickDisplay() }.apply { visibility = View.GONE }
        stats = TextView(this).apply {
            setTextColor(0xFF9AA3B2.toInt())
            textSize = 12f
            setPadding(dp(6), 0, dp(10), 0)
        }
        toolbar = bar(
            keyboardButton,
            button("Keys") { setKeysBar(keysBar.visibility != View.VISIBLE) },
            modeButton,
            button("Fit") { screen.fit() },
            button("1:1") { screen.actualSize() },
            captureButton,
            displayButton,
            button("Send clipboard") { sendClipboard(force = true) },
            stats,
            button("Hide") { setToolbar(false) },
            button("Disconnect") { finish() },
        )
        updateModeButton()

        val fkeys = (1..12).map { button("F$it") { keyboard.tap("F$it") } }.toTypedArray()
        keysBar = bar(
            button("Esc") { keyboard.tap("Escape") },
            button("Tab") { keyboard.tap("Tab") },
            modifier("Ctrl", "ControlLeft"),
            modifier("Alt", "AltLeft"),
            modifier("Shift", "ShiftLeft"),
            modifier("Win", "MetaLeft"),
            spacer(),
            repeating("←", "ArrowLeft"),
            repeating("↑", "ArrowUp"),
            repeating("↓", "ArrowDown"),
            repeating("→", "ArrowRight"),
            spacer(),
            button("Home") { keyboard.tap("Home") },
            button("End") { keyboard.tap("End") },
            button("PgUp") { keyboard.tap("PageUp") },
            button("PgDn") { keyboard.tap("PageDown") },
            spacer(),
            repeating("⌫", "Backspace"),
            repeating("Del", "Delete"),
            button("Ins") { keyboard.tap("Insert") },
            button("Enter") { keyboard.tap("Enter") },
            spacer(),
            *fkeys,
            spacer(),
            button("PrtSc") { keyboard.tap("PrintScreen") },
            button("Menu") { keyboard.tap("ContextMenu") },
        )

        column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(toolbar, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
            addView(screen, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
            addView(keysBar, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }

        handle = TextView(this).apply {
            text = "☰"
            textSize = 18f
            setTextColor(Color.WHITE)
            gravity = Gravity.CENTER
            setBackgroundResource(R.drawable.key_bg)
            alpha = 0.6f
            isFocusable = false
            setOnClickListener { setToolbar(true) }
        }
        notice = TextView(this).apply {
            setTextColor(Color.WHITE)
            textSize = 14f
            setPadding(dp(14), dp(8), dp(14), dp(8))
            setBackgroundResource(R.drawable.bar_bg)
            visibility = View.GONE
        }

        val root = FrameLayout(this).apply {
            setBackgroundColor(Color.BLACK)
            addView(column, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
            addView(handle, FrameLayout.LayoutParams(dp(40), dp(40), Gravity.TOP or Gravity.END).apply {
                setMargins(0, dp(8), dp(8), 0)
            })
            addView(notice, FrameLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.TOP or Gravity.CENTER_HORIZONTAL).apply {
                topMargin = dp(56)
            })
        }
        root.setOnApplyWindowInsetsListener { _, insets ->
            val ime = insets.getInsets(WindowInsets.Type.ime())
            val bars = insets.getInsets(WindowInsets.Type.systemBars() or WindowInsets.Type.displayCutout())
            column.setPadding(bars.left, bars.top, bars.right, maxOf(ime.bottom, bars.bottom))
            (handle.layoutParams as FrameLayout.LayoutParams).let {
                it.topMargin = bars.top + dp(8)
                it.marginEnd = bars.right + dp(8)
                handle.layoutParams = it
            }
            val visible = insets.isVisible(WindowInsets.Type.ime())
            if (visible != imeVisible) {
                imeVisible = visible
                keyboardButton.isActivated = visible
                updateKeysBar()
            }
            WindowInsets.CONSUMED
        }
        setContentView(root)
        setToolbar(prefs.toolbar)
        updateKeysBar()
        screen.requestFocus()
    }

    private fun hideSystemBars() {
        window.insetsController?.let {
            it.hide(WindowInsets.Type.systemBars())
            it.systemBarsBehavior = WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        }
    }

    private fun setToolbar(on: Boolean) {
        prefs.toolbar = on
        toolbar.visibility = if (on) View.VISIBLE else View.GONE
        handle.visibility = if (on) View.GONE else View.VISIBLE
    }

    private fun setKeysBar(on: Boolean) {
        prefs.keysBar = on
        updateKeysBar()
    }

    /** The keys bar shows with the on-screen keyboard, or always if switched on. */
    private fun updateKeysBar() {
        keysBar.visibility = if (prefs.keysBar || imeVisible) View.VISIBLE else View.GONE
    }

    private fun toggleMode() {
        screen.mode = if (screen.mode == ScreenView.Mode.TOUCH) ScreenView.Mode.TRACKPAD else ScreenView.Mode.TOUCH
        prefs.trackpad = screen.mode == ScreenView.Mode.TRACKPAD
        updateModeButton()
        showNotice(
            if (prefs.trackpad) "Trackpad: drag to move the pointer, tap to click, long-press to drag"
            else "Direct touch: tap to click where you touch, long-press for right click",
        )
    }

    private fun updateModeButton() {
        modeButton.text = if (screen.mode == ScreenView.Mode.TOUCH) "☝ Touch" else "▭ Trackpad"
    }

    private fun showLatches() {
        for ((code, view) in modifierViews) {
            val l = keyboard.latches[code]
            view.isActivated = l == RemoteKeyboard.Latch.ONCE
            view.isSelected = l == RemoteKeyboard.Latch.LOCKED
        }
    }

    private val hideNotice = Runnable { notice.visibility = View.GONE }

    private fun showNotice(text: String, ms: Long = 3500) {
        notice.text = text
        notice.visibility = View.VISIBLE
        main.removeCallbacks(hideNotice)
        if (ms > 0) main.postDelayed(hideNotice, ms)
    }

    private fun pickDisplay() {
        val names = Array(displayCount) { "Screen ${it + 1}" + if (it == display) " (showing)" else "" }
        AlertDialog.Builder(this)
            .setTitle("Show which screen?")
            .setItems(names) { _, which ->
                client?.send(JSONObject().put("type", "select_display").put("display", which))
            }
            .show()
    }

    private val statsTick = object : Runnable {
        override fun run() {
            val f = frames.getAndSet(0)
            val b = bytes.getAndSet(0)
            stats.text = if (client?.connected == true) {
                "%s · %d fps · %.1f Mbit/s".format(if (videoMode == true) "H.264" else "JPEG", f, b * 8 / 1e6)
            } else {
                ""
            }
            main.postDelayed(this, 1000)
        }
    }

    // ---- Connection ----

    override fun onStart() {
        super.onStart()
        clipboard.addPrimaryClipChangedListener(clipListener)
        connect()
    }

    override fun onStop() {
        super.onStop()
        clipboard.removePrimaryClipChangedListener(clipListener)
        keyboard.releaseAll()
        client?.close()
        client = null
        decoder.reset()
    }

    override fun onDestroy() {
        super.onDestroy()
        main.removeCallbacksAndMessages(null)
        decoder.quit()
    }

    private fun connect() {
        if (client != null) return
        showNotice("Connecting to $computer…", 0)
        client = try {
            RelayClient(computer, "", password, this).also { it.start() }
        } catch (e: IllegalArgumentException) {
            onEnded(e.message ?: "Bad address", false)
            null
        }
    }

    override fun onConnected(sessionId: String) {
        notice.visibility = View.GONE
        decoder.reset()
        decoderErrors = 0
        client?.refresh(useVideo)
        sendClipboard()
    }

    override fun onStatus(text: String?) {
        if (text != null) showNotice(text, 0) else notice.visibility = View.GONE
    }

    override fun onEnded(reason: String, wrongPassword: Boolean) {
        setResult(RESULT_OK, Intent().putExtra(EXTRA_REASON, reason).putExtra(EXTRA_WRONG_PASSWORD, wrongPassword)
            .putExtra(EXTRA_COMPUTER, computer))
        finish()
    }

    override fun onFrame(data: ByteArray) {
        bytes.addAndGet(data.size.toLong())
        when (data[0].toInt()) {
            'V'.code -> {
                if (data.size < 10) return
                if (videoMode != true) {
                    videoMode = true
                    main.post { screen.showVideo(true); screen.tiles.clear() }
                }
                contentSize(VideoDecoder.u16(data, 5), VideoDecoder.u16(data, 7))
                decoder.push(data)
            }
            'U'.code -> {
                if (videoMode != false) {
                    if (videoMode == true) decoder.reset()
                    videoMode = false
                    main.post { screen.showVideo(false) }
                }
                val r = screen.tiles.apply(data) ?: return
                contentSize(r.width, r.height)
                frames.incrementAndGet()
                client?.ack(r.seq)
            }
        }
    }

    private fun contentSize(w: Int, h: Int) {
        if (w == shownW && h == shownH) return
        shownW = w
        shownH = h
        main.post { screen.setContentSize(w, h) }
    }

    override fun onMessage(msg: JSONObject) {
        when (msg.optString("type")) {
            "cursor" -> screen.setCursorShape(msg.optString("cursor", "default"))
            "displays" -> {
                displayCount = msg.optInt("count", 1).coerceAtLeast(1)
                display = msg.optInt("display", 0)
                displayButton.text = "Screen ${display + 1}/$displayCount"
                displayButton.visibility = if (displayCount > 1) View.VISIBLE else View.GONE
            }
            "clipboard" -> {
                val text = msg.optString("text")
                lastClipboard = text
                clipboard.setPrimaryClip(ClipData.newPlainText("Remoter", text))
            }
        }
    }

    private fun videoFailed() {
        decoderErrors++
        if (decoderErrors >= 3 && useVideo) {
            useVideo = false
            showNotice("Video decoding failed, switched to JPEG")
        }
        client?.refresh(useVideo)
    }

    // ---- Clipboard ----

    /** Sends the phone's clipboard to the computer if it changed (Android only lets us read it while focused). */
    private fun sendClipboard(force: Boolean = false) {
        val c = client ?: return
        if (!c.connected) return
        val text = try {
            clipboard.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString()
        } catch (e: SecurityException) {
            null
        } ?: return
        if (!force && text == lastClipboard) return
        lastClipboard = text
        c.send(JSONObject().put("type", "clipboard").put("text", text))
        if (force) showNotice("Clipboard sent", 1500)
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) {
            hideSystemBars()
            sendClipboard()
            screen.requestFocus()
        } else {
            keyboard.releaseAll()
        }
    }

    private var backAt = 0L

    @Deprecated("Back from the edge swipe; a second one within two seconds leaves.")
    override fun onBackPressed() {
        // Edge swipes are easy to make by accident while working on the remote
        // screen, so leaving takes two.
        val now = android.os.SystemClock.uptimeMillis()
        if (now - backAt < 2000) {
            @Suppress("DEPRECATION")
            super.onBackPressed()
            return
        }
        backAt = now
        setToolbar(true)
        showNotice("Swipe back again to disconnect", 2000)
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        // Folding, unfolding and DeX all land here; the screen view refits on
        // its new size and the connection carries on.
        hideSystemBars()
    }

    // ---- ScreenView.Remote ----

    override fun move(x: Float, y: Float) {
        client?.input("mousemove") {
            put("x", x.toDouble())
            put("y", y.toDouble())
        }
    }

    override fun button(button: Int, down: Boolean, x: Float, y: Float) {
        if (down) keyboard.mouseDown()
        client?.input(if (down) "mousedown" else "mouseup") {
            put("button", button)
            put("x", x.toDouble())
            put("y", y.toDouble())
        }
        if (!down) keyboard.mouseUp()
    }

    override fun wheel(deltaY: Float, x: Float, y: Float) {
        client?.input("wheel") {
            put("deltaY", deltaY.toDouble())
            put("x", x.toDouble())
            put("y", y.toDouble())
        }
    }

    override fun toggleKeyboard() {
        val c = window.insetsController ?: return
        if (imeVisible) {
            c.hide(WindowInsets.Type.ime())
        } else {
            screen.requestFocus()
            c.show(WindowInsets.Type.ime())
        }
    }

    override fun surfaceChanged(surface: Surface?) {
        surfaceReady = surface != null
        decoder.setSurface(surface)
        if (surface != null && client?.connected == true && useVideo) client?.refresh(true)
    }

    override fun captureChanged(captured: Boolean) {
        captureButton.isActivated = captured
        captureButton.text = if (captured) "Unlock mouse" else "Lock mouse"
        if (captured) showNotice("Mouse locked to the remote screen. Ctrl+Alt+Esc unlocks it.")
    }

    override fun hardwareKey(event: KeyEvent): Boolean = keyboard.hardware(event)

    override fun createInputConnection(info: EditorInfo): InputConnection = keyboard.inputConnection(screen, info)

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        // Hardware keys normally reach the screen before the on-screen
        // keyboard; this catches them when something else has focus.
        if (keyboard.hardware(event)) return true
        return super.dispatchKeyEvent(event)
    }

    companion object {
        const val EXTRA_COMPUTER = "computer"
        const val EXTRA_PASSWORD = "password"
        const val EXTRA_REASON = "reason"
        const val EXTRA_WRONG_PASSWORD = "wrongPassword"
    }
}
