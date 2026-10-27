package io.github.aleynikovich.remoter

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Matrix
import android.graphics.SurfaceTexture
import android.os.SystemClock
import android.view.HapticFeedbackConstants
import android.view.InputDevice
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.PointerIcon
import android.view.Surface
import android.view.TextureView
import android.view.ViewConfiguration
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection
import android.widget.FrameLayout
import kotlin.math.abs
import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min

/**
 * The remote screen: shows the picture (H.264 on a TextureView, or JPEG
 * tiles), handles zoom and pan, and turns touch, mouse and pointer-capture
 * input into remote mouse events.
 *
 * Touch gestures, in both modes:
 *  - pinch: zoom (and pan while pinching)
 *  - two-finger drag: scroll the wheel
 *  - two-finger tap: right click
 *  - three-finger drag: pan the zoomed picture
 *  - three-finger tap: show or hide the keyboard
 *
 * Direct touch: tap clicks where you tap, drag drags there, long press right
 * clicks. Trackpad: one finger moves the pointer, tap clicks, long press (or
 * tap then touch again) holds the button for dragging.
 */
@SuppressLint("ViewConstructor")
class ScreenView(context: Context, private val remote: Remote) : FrameLayout(context) {

    /** Where input goes. Coordinates are 0..1 of the remote screen. */
    interface Remote {
        fun move(x: Float, y: Float)
        fun button(button: Int, down: Boolean, x: Float, y: Float)
        fun wheel(deltaY: Float, x: Float, y: Float)
        fun toggleKeyboard()
        fun surfaceChanged(surface: Surface?)
        fun captureChanged(captured: Boolean)
        /** A key from a hardware keyboard, before the on-screen keyboard sees it. */
        fun hardwareKey(event: KeyEvent): Boolean
        fun createInputConnection(info: EditorInfo): InputConnection
    }

    enum class Mode { TOUCH, TRACKPAD }

    val video = TextureView(context)
    val tiles = TileView(context)
    private val cursor = CursorView(context)

    var mode = Mode.TOUCH
        set(value) {
            field = value
            updateCursorShown()
        }

    private var contentW = 0
    private var contentH = 0
    private var zoom = 1f
    private var tx = 0f
    private var ty = 0f
    private var curX = 0f
    private var curY = 0f

    private val density = resources.displayMetrics.density
    private val slop = ViewConfiguration.get(context).scaledTouchSlop.toFloat()
    private val longPressMs = 450L
    private val tapMs = 280L

    init {
        isFocusable = true
        isFocusableInTouchMode = true
        addView(video, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
        addView(tiles, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
        addView(cursor, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
        tiles.visibility = INVISIBLE
        video.isOpaque = true
        video.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            var surface: Surface? = null
            override fun onSurfaceTextureAvailable(st: SurfaceTexture, w: Int, h: Int) {
                surface = Surface(st)
                remote.surfaceChanged(surface)
            }

            override fun onSurfaceTextureSizeChanged(st: SurfaceTexture, w: Int, h: Int) {}

            override fun onSurfaceTextureDestroyed(st: SurfaceTexture): Boolean {
                remote.surfaceChanged(null)
                surface?.release()
                surface = null
                return true
            }

            override fun onSurfaceTextureUpdated(st: SurfaceTexture) {}
        }
    }

    /** Shows the H.264 picture or the JPEG tiles. */
    fun showVideo(on: Boolean) {
        tiles.visibility = if (on) INVISIBLE else VISIBLE
    }

    fun setContentSize(w: Int, h: Int) {
        if (w == contentW && h == contentH) return
        val first = contentW == 0
        contentW = w
        contentH = h
        zoom = 1f
        updateCursorShown()
        if (first) {
            curX = w / 2f
            curY = h / 2f
        } else {
            curX = curX.coerceIn(0f, w - 1f)
            curY = curY.coerceIn(0f, h - 1f)
        }
        applyTransform()
    }

    fun setCursorShape(name: String) {
        cursor.shape = name
        pointerIcon = PointerIcon.getSystemIcon(context, pointerType(name))
    }

    /** Back to the whole screen in view. */
    fun fit() {
        zoom = 1f
        applyTransform()
    }

    /** One remote pixel per screen pixel, around the pointer. */
    fun actualSize() {
        if (contentW == 0) return
        zoom = max(1f, 1f / fitScale())
        val s = scale()
        tx = width / 2f - curX * s
        ty = height / 2f - curY * s
        applyTransform()
    }

    // ---- Geometry ----

    private fun fitScale(): Float =
        if (contentW == 0 || width == 0) 1f else min(width.toFloat() / contentW, height.toFloat() / contentH)

    private fun scale() = fitScale() * zoom

    private fun applyTransform() {
        if (contentW == 0 || width == 0 || height == 0) return
        val s = scale()
        val cw = contentW * s
        val ch = contentH * s
        tx = if (cw <= width) (width - cw) / 2 else tx.coerceIn(width - cw, 0f)
        ty = if (ch <= height) (height - ch) / 2 else ty.coerceIn(height - ch, 0f)
        // The texture is first stretched over the whole view, then this applies.
        val m = Matrix()
        m.setScale(cw / width, ch / height)
        m.postTranslate(tx, ty)
        video.setTransform(m)
        video.invalidate()
        val cm = Matrix()
        cm.setScale(s, s)
        cm.postTranslate(tx, ty)
        tiles.setContentMatrix(cm)
        cursor.moveTo(tx + curX * s, ty + curY * s)
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        if (contentW == 0 || oldw == 0 || oldh == 0) {
            applyTransform()
            return
        }
        // Keep what was in the middle in the middle, e.g. when unfolding or
        // when the keyboard opens.
        val oldFit = min(oldw.toFloat() / contentW, oldh.toFloat() / contentH)
        val oldScale = oldFit * zoom
        val cx = (oldw / 2f - tx) / oldScale
        val cy = (oldh / 2f - ty) / oldScale
        val s = scale()
        tx = w / 2f - cx * s
        ty = h / 2f - cy * s
        applyTransform()
        ensureCursorVisible()
    }

    private fun contentX(vx: Float) = ((vx - tx) / scale()).coerceIn(0f, max(contentW - 1f, 0f))
    private fun contentY(vy: Float) = ((vy - ty) / scale()).coerceIn(0f, max(contentH - 1f, 0f))
    private fun nx() = if (contentW > 0) curX / contentW else 0f
    private fun ny() = if (contentH > 0) curY / contentH else 0f

    /** Moves the pointer to a content position and tells the host. */
    private fun pointTo(cx: Float, cy: Float) {
        curX = cx.coerceIn(0f, max(contentW - 1f, 0f))
        curY = cy.coerceIn(0f, max(contentH - 1f, 0f))
        val s = scale()
        cursor.moveTo(tx + curX * s, ty + curY * s)
        remote.move(nx(), ny())
    }

    /** Pans a zoomed picture so the pointer stays on screen. */
    private fun ensureCursorVisible() {
        if (zoom <= 1f) return
        val s = scale()
        val margin = 40 * density
        val vx = tx + curX * s
        val vy = ty + curY * s
        if (vx < margin) tx += margin - vx else if (vx > width - margin) tx -= vx - (width - margin)
        if (vy < margin) ty += margin - vy else if (vy > height - margin) ty -= vy - (height - margin)
        applyTransform()
    }

    private fun setZoomAround(newZoom: Float, anchorCx: Float, anchorCy: Float, fx: Float, fy: Float) {
        val maxZoom = max(4f, 3f / fitScale())
        zoom = newZoom.coerceIn(1f, maxZoom)
        val s = scale()
        tx = fx - anchorCx * s
        ty = fy - anchorCy * s
        applyTransform()
    }

    // ---- Pointer shown on the picture ----

    private var mouseHover = false
    private var captured = false

    private fun updateCursorShown() {
        cursor.shown = contentW > 0 && (captured || !mouseHover)
    }

    // ---- Touch ----

    private enum class G { IDLE, PENDING, DRAG, MOVE, HOLD, TWO, PINCH, SCROLL, THREE, DONE }

    private var g = G.IDLE
    private var downX = 0f
    private var downY = 0f
    private var downTime = 0L
    private var lastX = 0f
    private var lastY = 0f
    private var lastMoveTime = 0L
    private var tapAgain = false
    private var lastTapTime = 0L
    private var lastTapX = 0f
    private var lastTapY = 0f
    private var lastTapCx = 0f
    private var lastTapCy = 0f
    private var span0 = 0f
    private var focal0X = 0f
    private var focal0Y = 0f
    private var focalX = 0f
    private var focalY = 0f
    private var multiStart = 0L
    private var zoom0 = 1f
    private var anchorX = 0f
    private var anchorY = 0f
    private var wheelAcc = 0f
    private var buttonHeld = false

    private val longPress = Runnable {
        if (g != G.PENDING) return@Runnable
        performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
        if (mode == Mode.TOUCH) {
            pointTo(contentX(downX), contentY(downY))
            click(2)
            g = G.DONE
        } else {
            press(true)
            g = G.HOLD
        }
    }

    private fun press(down: Boolean) {
        if (buttonHeld == down) return
        buttonHeld = down
        remote.button(0, down, nx(), ny())
    }

    private fun click(button: Int) {
        remote.button(button, true, nx(), ny())
        remote.button(button, false, nx(), ny())
    }

    @SuppressLint("ClickableViewAccessibility")
    override fun onTouchEvent(e: MotionEvent): Boolean {
        if (e.getToolType(0) == MotionEvent.TOOL_TYPE_MOUSE) return mouse(e)
        if (contentW == 0) return true
        if (mouseHover) {
            mouseHover = false
            updateCursorShown()
        }
        val now = SystemClock.uptimeMillis()
        when (e.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                requestFocus()
                g = G.PENDING
                downX = e.x; downY = e.y
                lastX = e.x; lastY = e.y
                downTime = now
                lastMoveTime = now
                tapAgain = mode == Mode.TRACKPAD && now - lastTapTime < 300 &&
                    hypot(e.x - lastTapX, e.y - lastTapY) < 60 * density
                postDelayed(longPress, longPressMs)
            }

            MotionEvent.ACTION_POINTER_DOWN -> {
                removeCallbacks(longPress)
                if (g == G.DRAG || g == G.HOLD) press(false)
                if (e.pointerCount == 2 && g in setOf(G.PENDING, G.DRAG, G.MOVE, G.HOLD, G.IDLE)) {
                    g = G.TWO
                    multiStart = now
                    span0 = span(e)
                    focus(e)
                    focal0X = focalX; focal0Y = focalY
                    zoom0 = zoom
                    wheelAcc = 0f
                } else if (e.pointerCount >= 3 && g in setOf(G.TWO, G.PENDING)) {
                    g = G.THREE
                    focus(e)
                    focal0X = focalX; focal0Y = focalY
                } else if (e.pointerCount >= 3 && g != G.THREE) {
                    g = G.DONE
                }
            }

            MotionEvent.ACTION_MOVE -> moveGesture(e, now)

            MotionEvent.ACTION_POINTER_UP -> {
                when (g) {
                    G.TWO -> {
                        if (now - multiStart < 350) {
                            performHapticFeedback(HapticFeedbackConstants.CONTEXT_CLICK)
                            if (mode == Mode.TOUCH) pointTo(contentX(focal0X), contentY(focal0Y))
                            click(2)
                        }
                        g = G.DONE
                    }
                    G.THREE -> {
                        if (now - multiStart < 400 && hypot(focalX - focal0X, focalY - focal0Y) < slop) {
                            remote.toggleKeyboard()
                        }
                        g = G.DONE
                    }
                    G.PINCH, G.SCROLL -> g = G.DONE
                    else -> {}
                }
            }

            MotionEvent.ACTION_UP -> {
                removeCallbacks(longPress)
                when (g) {
                    G.PENDING -> {
                        if (mode == Mode.TOUCH) {
                            // A second tap near the first lands on the same spot, so
                            // it reads as a double click.
                            if (now - lastTapTime < 400 && hypot(e.x - lastTapX, e.y - lastTapY) < 30 * density) {
                                pointTo(lastTapCx, lastTapCy)
                            } else {
                                pointTo(contentX(e.x), contentY(e.y))
                            }
                            lastTapCx = curX; lastTapCy = curY
                        }
                        click(0)
                        lastTapTime = now
                        lastTapX = e.x; lastTapY = e.y
                    }
                    G.DRAG -> {
                        pointTo(contentX(e.x), contentY(e.y))
                        press(false)
                    }
                    G.HOLD -> press(false)
                    else -> {}
                }
                g = G.IDLE
            }

            MotionEvent.ACTION_CANCEL -> {
                removeCallbacks(longPress)
                press(false)
                g = G.IDLE
            }
        }
        return true
    }

    private fun moveGesture(e: MotionEvent, now: Long) {
        when (g) {
            G.PENDING -> {
                if (hypot(e.x - downX, e.y - downY) < slop) return
                removeCallbacks(longPress)
                if (mode == Mode.TOUCH) {
                    pointTo(contentX(downX), contentY(downY))
                    press(true)
                    g = G.DRAG
                    pointTo(contentX(e.x), contentY(e.y))
                } else {
                    if (tapAgain) press(true)
                    g = if (tapAgain) G.HOLD else G.MOVE
                    relative(e, now)
                }
            }
            G.DRAG -> pointTo(contentX(e.x), contentY(e.y))
            G.MOVE, G.HOLD -> relative(e, now)
            G.TWO -> {
                focus(e)
                val span = span(e)
                if (abs(span - span0) > slop * 2) {
                    g = G.PINCH
                    span0 = span
                    zoom0 = zoom
                    focal0X = focalX; focal0Y = focalY
                    val s = scale()
                    anchorX = (focalX - tx) / s
                    anchorY = (focalY - ty) / s
                } else if (hypot(focalX - focal0X, focalY - focal0Y) > slop) {
                    g = G.SCROLL
                    if (mode == Mode.TOUCH) pointTo(contentX(focal0X), contentY(focal0Y))
                    lastY = focalY
                }
            }
            G.PINCH -> {
                focus(e)
                val span = span(e)
                if (span0 > 0) setZoomAround(zoom0 * span / span0, anchorX, anchorY, focalX, focalY)
            }
            G.SCROLL -> {
                focus(e)
                // Natural scrolling: content follows the fingers.
                wheelAcc += (lastY - focalY) * 2f / density
                lastY = focalY
                if (abs(wheelAcc) >= 25) {
                    remote.wheel(wheelAcc, nx(), ny())
                    wheelAcc = 0f
                }
            }
            G.THREE -> {
                val px = focalX
                val py = focalY
                focus(e)
                tx += focalX - px
                ty += focalY - py
                applyTransform()
            }
            else -> {}
        }
    }

    /** Trackpad motion: the pointer moves by the finger's motion, faster for quick flicks. */
    private fun relative(e: MotionEvent, now: Long) {
        val dx = e.x - lastX
        val dy = e.y - lastY
        val dt = max(1L, now - lastMoveTime)
        lastX = e.x; lastY = e.y
        lastMoveTime = now
        val speed = hypot(dx, dy) / dt / density // dp per ms
        val gain = (1f + min(speed * 0.9f, 3f)) / scale()
        pointTo(curX + dx * gain, curY + dy * gain)
        ensureCursorVisible()
    }

    private fun focus(e: MotionEvent) {
        var sx = 0f
        var sy = 0f
        for (i in 0 until e.pointerCount) {
            sx += e.getX(i); sy += e.getY(i)
        }
        focalX = sx / e.pointerCount; focalY = sy / e.pointerCount
    }

    private fun span(e: MotionEvent): Float =
        if (e.pointerCount < 2) 0f else hypot(e.getX(0) - e.getX(1), e.getY(0) - e.getY(1))

    // ---- Mouse (DeX, Bluetooth, USB) ----

    private var mouseButtons = 0

    private fun buttons(e: MotionEvent) {
        val now = e.buttonState
        val changed = now xor mouseButtons
        mouseButtons = now
        for ((bit, button) in listOf(
            MotionEvent.BUTTON_PRIMARY to 0,
            MotionEvent.BUTTON_TERTIARY to 1,
            MotionEvent.BUTTON_SECONDARY to 2,
        )) {
            if (changed and bit != 0) remote.button(button, now and bit != 0, nx(), ny())
        }
    }

    private fun mouse(e: MotionEvent): Boolean {
        if (contentW == 0) return true
        if (!mouseHover) {
            mouseHover = true
            updateCursorShown()
        }
        when (e.actionMasked) {
            MotionEvent.ACTION_SCROLL -> {
                val v = e.getAxisValue(MotionEvent.AXIS_VSCROLL)
                if (v != 0f) remote.wheel(-v * 100, nx(), ny())
                return true
            }
            MotionEvent.ACTION_HOVER_EXIT -> return true
        }
        val cx = contentX(e.x)
        val cy = contentY(e.y)
        if (cx != curX || cy != curY) pointTo(cx, cy)
        if (e.actionMasked == MotionEvent.ACTION_DOWN) requestFocus()
        buttons(e)
        return true
    }

    override fun onGenericMotionEvent(e: MotionEvent): Boolean {
        if (e.isFromSource(InputDevice.SOURCE_MOUSE) || e.isFromSource(InputDevice.SOURCE_TOUCHPAD)) {
            return mouse(e)
        }
        return super.onGenericMotionEvent(e)
    }

    override fun onResolvePointerIcon(event: MotionEvent, pointerIndex: Int): PointerIcon? =
        if (captured) null else pointerIcon

    // ---- Pointer capture: the mouse only moves the remote pointer ----

    val isCaptured get() = captured

    fun capture(on: Boolean) {
        if (on) {
            requestFocus()
            requestPointerCapture()
        } else {
            releasePointerCapture()
        }
    }

    override fun onPointerCaptureChange(hasCapture: Boolean) {
        captured = hasCapture
        if (!hasCapture && mouseButtons != 0) {
            // Let go of whatever was held when capture ended.
            for ((bit, button) in listOf(MotionEvent.BUTTON_PRIMARY to 0, MotionEvent.BUTTON_TERTIARY to 1, MotionEvent.BUTTON_SECONDARY to 2)) {
                if (mouseButtons and bit != 0) remote.button(button, false, nx(), ny())
            }
            mouseButtons = 0
        }
        updateCursorShown()
        remote.captureChanged(hasCapture)
    }

    override fun onCapturedPointerEvent(e: MotionEvent): Boolean {
        if (contentW == 0) return true
        when (e.actionMasked) {
            MotionEvent.ACTION_SCROLL -> {
                val v = e.getAxisValue(MotionEvent.AXIS_VSCROLL)
                if (v != 0f) remote.wheel(-v * 100, nx(), ny())
                return true
            }
        }
        var dx = e.x
        var dy = e.y
        for (h in 0 until e.historySize) {
            dx += e.getHistoricalX(h)
            dy += e.getHistoricalY(h)
        }
        if (dx != 0f || dy != 0f) {
            val speed = hypot(dx, dy)
            val gain = 1f + min(speed / 12f, 2f)
            pointTo(curX + dx * gain, curY + dy * gain)
            ensureCursorVisible()
        }
        buttons(e)
        return true
    }

    // ---- Keyboard ----

    override fun onCheckIsTextEditor() = true

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection = remote.createInputConnection(outAttrs)

    override fun dispatchKeyEventPreIme(event: KeyEvent): Boolean =
        remote.hardwareKey(event) || super.dispatchKeyEventPreIme(event)

    companion object {
        fun pointerType(css: String) = when (css) {
            "text" -> PointerIcon.TYPE_TEXT
            "pointer" -> PointerIcon.TYPE_HAND
            "wait" -> PointerIcon.TYPE_WAIT
            "progress" -> PointerIcon.TYPE_WAIT
            "crosshair" -> PointerIcon.TYPE_CROSSHAIR
            "move" -> PointerIcon.TYPE_ALL_SCROLL
            "ns-resize" -> PointerIcon.TYPE_VERTICAL_DOUBLE_ARROW
            "ew-resize" -> PointerIcon.TYPE_HORIZONTAL_DOUBLE_ARROW
            "nwse-resize" -> PointerIcon.TYPE_TOP_LEFT_DIAGONAL_DOUBLE_ARROW
            "nesw-resize" -> PointerIcon.TYPE_TOP_RIGHT_DIAGONAL_DOUBLE_ARROW
            "not-allowed" -> PointerIcon.TYPE_NO_DROP
            "help" -> PointerIcon.TYPE_HELP
            "none" -> PointerIcon.TYPE_NULL
            else -> PointerIcon.TYPE_ARROW
        }
    }
}
