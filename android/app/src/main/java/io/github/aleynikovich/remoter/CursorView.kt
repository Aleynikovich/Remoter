package io.github.aleynikovich.remoter

import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Path
import android.view.View

/**
 * Draws the remote mouse pointer. The host does not put the pointer into the
 * picture, it only says what shape it has, so touch and captured mouse input
 * need a pointer drawn here to show where clicks will land.
 */
class CursorView(context: Context) : View(context) {
    private val density = resources.displayMetrics.density
    private val fill = Paint(Paint.ANTI_ALIAS_FLAG).apply { color = 0xFFFFFFFF.toInt() }
    private val stroke = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = 0xFF000000.toInt()
        style = Paint.Style.STROKE
        strokeWidth = 1.2f * density
        strokeJoin = Paint.Join.ROUND
    }
    private val arrow = Path().apply {
        // Tip at 0,0, in dp.
        moveTo(0f, 0f); lineTo(0f, 17f); lineTo(4.2f, 13.2f); lineTo(7f, 19.5f)
        lineTo(9.6f, 18.4f); lineTo(6.9f, 12.2f); lineTo(12.4f, 12.2f); close()
    }
    private val beam = Path().apply {
        moveTo(-3.5f, -9f); lineTo(3.5f, -9f); moveTo(0f, -9f); lineTo(0f, 9f)
        moveTo(-3.5f, 9f); lineTo(3.5f, 9f)
    }
    private val cross = Path().apply {
        moveTo(-9f, 0f); lineTo(9f, 0f); moveTo(0f, -9f); lineTo(0f, 9f)
    }

    var shape = "default"
        set(value) {
            field = value
            invalidate()
        }

    private var px = 0f
    private var py = 0f
    var shown = false
        set(value) {
            if (field != value) {
                field = value
                invalidate()
            }
        }

    /** Moves the pointer to a view position. */
    fun moveTo(x: Float, y: Float) {
        px = x
        py = y
        if (shown) invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        if (!shown || shape == "none") return
        canvas.save()
        canvas.translate(px, py)
        canvas.scale(density, density)
        when (shape) {
            "text" -> drawLines(canvas, beam)
            "crosshair", "move" -> drawLines(canvas, cross)
            else -> {
                canvas.drawPath(arrow, fill)
                stroke.strokeWidth = 1.2f
                canvas.drawPath(arrow, stroke)
            }
        }
        canvas.restore()
    }

    private fun drawLines(canvas: Canvas, path: Path) {
        stroke.color = 0xFF000000.toInt()
        stroke.strokeWidth = 3.2f
        canvas.drawPath(path, stroke)
        stroke.color = 0xFFFFFFFF.toInt()
        stroke.strokeWidth = 1.4f
        canvas.drawPath(path, stroke)
        stroke.color = 0xFF000000.toInt()
    }
}
