package io.github.aleynikovich.remoter

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Matrix
import android.graphics.Paint
import android.view.View

/**
 * Shows the JPEG tile stream ('U' updates), the fallback when H.264 is off or
 * not possible. Each update only paints the rectangles that changed, so the
 * last picture is kept in [frame] and tiles are drawn over it.
 */
class TileView(context: Context) : View(context) {
    private val lock = Any()
    private var frame: Bitmap? = null
    private var frameCanvas: Canvas? = null
    private val paint = Paint(Paint.FILTER_BITMAP_FLAG)
    private val contentMatrix = Matrix()

    /** Where content pixels go on the view; set by [ScreenView]. */
    fun setContentMatrix(m: Matrix) {
        contentMatrix.set(m)
        invalidate()
    }

    class Result(val seq: Long, val width: Int, val height: Int)

    /**
     * Paints a 'U' update (the whole binary message) and returns its sequence
     * number and the screen size. Network thread.
     */
    fun apply(msg: ByteArray): Result? {
        if (msg.size < 11) return null
        val seq = VideoDecoder.u32(msg, 1)
        val width = VideoDecoder.u16(msg, 5)
        val height = VideoDecoder.u16(msg, 7)
        val count = VideoDecoder.u16(msg, 9)
        if (width == 0 || height == 0) return null
        val opts = BitmapFactory.Options().apply { inPreferredConfig = Bitmap.Config.ARGB_8888 }
        var off = 11
        val tiles = ArrayList<Triple<Int, Int, Bitmap>>(count)
        repeat(count) {
            if (off + 12 > msg.size) return@repeat
            val x = VideoDecoder.u16(msg, off)
            val y = VideoDecoder.u16(msg, off + 2)
            val len = VideoDecoder.u32(msg, off + 8).toInt()
            off += 12
            if (len < 0 || off + len > msg.size) return@repeat
            BitmapFactory.decodeByteArray(msg, off, len, opts)?.let { tiles.add(Triple(x, y, it)) }
            off += len
        }
        synchronized(lock) {
            var f = frame
            if (f == null || f.width != width || f.height != height) {
                // The old picture may still be on its way to the screen, so it
                // is left to the garbage collector rather than recycled.
                f = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
                frame = f
                frameCanvas = Canvas(f)
            }
            for ((x, y, b) in tiles) {
                frameCanvas!!.drawBitmap(b, x.toFloat(), y.toFloat(), null)
                b.recycle()
            }
        }
        postInvalidateOnAnimation()
        return Result(seq, width, height)
    }

    fun clear() {
        synchronized(lock) {
            frame = null
            frameCanvas = null
        }
        postInvalidate()
    }

    override fun onDraw(canvas: Canvas) {
        synchronized(lock) {
            val f = frame ?: return
            canvas.save()
            canvas.concat(contentMatrix)
            canvas.drawBitmap(f, 0f, 0f, paint)
            canvas.restore()
        }
    }
}
