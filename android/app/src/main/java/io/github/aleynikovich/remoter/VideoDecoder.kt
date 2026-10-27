package io.github.aleynikovich.remoter

import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaCodecList
import android.media.MediaFormat
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import android.view.Surface
import java.nio.ByteBuffer

/**
 * Decodes the host's H.264 'V' frames with the phone's hardware decoder
 * (MediaCodec) straight onto a Surface.
 *
 * Every frame is acknowledged once it has been drawn, so the host's flow
 * control (a window of three updates) follows what the screen really shows.
 * Hardware decoders may hold a frame or two back; those are acknowledged early
 * so the host never stalls waiting for them.
 */
class VideoDecoder(
    private val ack: (Long) -> Unit,
    /** The decoder failed; the session should ask for a new key frame or fall back to JPEG. */
    private val onError: () -> Unit,
    /** A frame was drawn. Decoder thread. */
    private val onFrameDrawn: () -> Unit,
) {
    private class Frame(val seq: Long, val width: Int, val height: Int, val key: Boolean, val data: ByteArray)

    private val thread = HandlerThread("h264").apply { start() }
    private val handler = Handler(thread.looper)

    // Everything below is only touched on the decoder thread.
    private var codec: MediaCodec? = null
    private var surface: Surface? = null
    private var width = 0
    private var height = 0
    private var needKey = true
    private val freeInputs = ArrayDeque<Int>()
    private val pending = ArrayDeque<Frame>()
    private val inDecoder = ArrayDeque<Long>()
    private var lastAcked = -1L

    /** Queues a 'V' message (the whole binary message, header included). Any thread. */
    fun push(msg: ByteArray) {
        if (msg.size < 10) return
        val seq = u32(msg, 1)
        val w = u16(msg, 5)
        val h = u16(msg, 7)
        val key = msg[9].toInt() and 1 != 0
        val frame = Frame(seq, w, h, key, msg.copyOfRange(10, msg.size))
        handler.post { onFrame(frame) }
    }

    /** Sets (or clears) the surface to draw on. A new surface needs a new key frame. */
    fun setSurface(s: Surface?) = handler.post {
        surface = s
        release()
    }

    /**
     * Drops the decoder, e.g. when the host switched to JPEG tiles or the
     * connection was replaced. The next key frame starts a new one.
     */
    fun reset() = handler.post {
        release()
        lastAcked = -1L
    }

    fun quit() {
        handler.post { release() }
        thread.quitSafely()
    }

    private fun onFrame(f: Frame) {
        if (surface == null) {
            ackUpTo(f.seq)
            return
        }
        if (f.key && (codec == null || f.width != width || f.height != height)) start(f)
        if (f.key) needKey = false
        if (codec == null || needKey) {
            // Nothing to draw it with yet; keep the host moving.
            ackUpTo(f.seq)
            return
        }
        pending.addLast(f)
        feed()
    }

    private fun start(f: Frame) {
        release()
        val (sps, pps) = parameterSets(f.data)
        if (sps == null || pps == null) {
            Log.w(TAG, "key frame without SPS/PPS")
            return
        }
        val format = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, f.width, f.height).apply {
            setByteBuffer("csd-0", ByteBuffer.wrap(sps))
            setByteBuffer("csd-1", ByteBuffer.wrap(pps))
            setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, maxOf(f.width * f.height, 1 shl 20))
            setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
            setInteger(MediaFormat.KEY_PRIORITY, 0) // real time
            // Vendor switches for decoders that predate KEY_LOW_LATENCY.
            setInteger("vendor.qti-ext-dec-low-latency.enable", 1)
            setInteger("vendor.low-latency.enable", 1)
        }
        try {
            val c = MediaCodec.createByCodecName(decoderName(f.width, f.height))
            c.setCallback(Callback(c), handler)
            c.configure(format, surface, null, 0)
            c.start()
            codec = c
            width = f.width
            height = f.height
            Log.i(TAG, "decoding ${f.width}x${f.height} with ${c.name}")
        } catch (e: Exception) {
            Log.e(TAG, "cannot start decoder", e)
            release()
            onError()
        }
    }

    private fun feed() {
        val c = codec ?: return
        while (freeInputs.isNotEmpty() && pending.isNotEmpty()) {
            val f = pending.removeFirst()
            val index = freeInputs.removeFirst()
            try {
                val buf = c.getInputBuffer(index) ?: continue
                if (f.data.size > buf.capacity()) {
                    // Should not happen with KEY_MAX_INPUT_SIZE; start over at the next key frame.
                    c.queueInputBuffer(index, 0, 0, f.seq, 0)
                    ackUpTo(f.seq)
                    needKey = true
                    onError()
                    continue
                }
                buf.clear()
                buf.put(f.data)
                c.queueInputBuffer(index, 0, f.data.size, f.seq, if (f.key) MediaCodec.BUFFER_FLAG_KEY_FRAME else 0)
                inDecoder.addLast(f.seq)
            } catch (e: Exception) {
                Log.e(TAG, "queue input", e)
                failed()
                return
            }
        }
        while (inDecoder.size > 2) ackUpTo(inDecoder.first())
    }

    private fun ackUpTo(seq: Long) {
        while (inDecoder.isNotEmpty() && inDecoder.first() <= seq) inDecoder.removeFirst()
        if (seq > lastAcked) {
            lastAcked = seq
            ack(seq)
        }
    }

    private fun failed() {
        release()
        onError()
    }

    private fun release() {
        codec?.let {
            try {
                it.stop()
            } catch (_: Exception) {
            }
            it.release()
        }
        codec = null
        needKey = true
        freeInputs.clear()
        // Whatever was waiting will never be drawn; let the host move on.
        pending.lastOrNull()?.let { ackUpTo(it.seq) }
        inDecoder.lastOrNull()?.let { ackUpTo(it) }
        pending.clear()
        inDecoder.clear()
    }

    private inner class Callback(private val c: MediaCodec) : MediaCodec.Callback() {
        override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {
            if (c !== this@VideoDecoder.codec) return
            freeInputs.addLast(index)
            feed()
        }

        override fun onOutputBufferAvailable(codec: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            if (c !== this@VideoDecoder.codec) return
            try {
                codec.releaseOutputBuffer(index, info.size > 0)
            } catch (e: Exception) {
                return
            }
            ackUpTo(info.presentationTimeUs)
            if (info.size > 0) onFrameDrawn()
        }

        override fun onError(codec: MediaCodec, e: MediaCodec.CodecException) {
            if (c !== this@VideoDecoder.codec) return
            Log.e(TAG, "decoder error", e)
            failed()
        }

        override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {}
    }

    companion object {
        private const val TAG = "VideoDecoder"

        /** Whether this phone has an H.264 decoder at all. */
        val supported: Boolean by lazy {
            MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.any { info ->
                !info.isEncoder && info.supportedTypes.any { it.equals(MediaFormat.MIMETYPE_VIDEO_AVC, true) }
            }
        }

        /**
         * Picks a hardware H.264 decoder that handles w×h, falling back to any
         * decoder that does.
         */
        fun decoderName(w: Int, h: Int): String {
            val candidates = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.filter { info ->
                !info.isEncoder && info.supportedTypes.any { it.equals(MediaFormat.MIMETYPE_VIDEO_AVC, true) }
            }
            fun fits(info: MediaCodecInfo) = try {
                info.getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC).videoCapabilities.isSizeSupported(w, h)
            } catch (e: Exception) {
                false
            }
            fun secure(info: MediaCodecInfo) = info.name.endsWith(".secure") || try {
                info.getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC)
                    .isFeatureRequired(MediaCodecInfo.CodecCapabilities.FEATURE_SecurePlayback)
            } catch (e: Exception) {
                false
            }
            val usable = candidates.filterNot(::secure)
            // Some Qualcomm phones list a separate low-latency variant.
            val best = usable.firstOrNull { it.isHardwareAccelerated && it.name.contains("low_latency") && fits(it) }
                ?: usable.firstOrNull { it.isHardwareAccelerated && fits(it) }
                ?: usable.firstOrNull { fits(it) }
                ?: usable.firstOrNull()
                ?: throw IllegalStateException("no H.264 decoder")
            return best.name
        }

        /** Returns the SPS and PPS NAL units (with start codes) from an Annex B frame. */
        fun parameterSets(data: ByteArray): Pair<ByteArray?, ByteArray?> {
            var sps: ByteArray? = null
            var pps: ByteArray? = null
            val starts = mutableListOf<Int>() // offsets of NAL headers
            var i = 0
            while (i + 3 < data.size) {
                if (data[i].toInt() == 0 && data[i + 1].toInt() == 0 && data[i + 2].toInt() == 1) {
                    starts.add(i + 3)
                    i += 3
                } else {
                    i++
                }
            }
            for ((n, start) in starts.withIndex()) {
                var end = if (n + 1 < starts.size) starts[n + 1] - 3 else data.size
                // A four byte start code leaves one more zero behind.
                while (end > start && data[end - 1].toInt() == 0) end--
                val unit = byteArrayOf(0, 0, 0, 1) + data.copyOfRange(start, end)
                when (data[start].toInt() and 0x1f) {
                    7 -> if (sps == null) sps = unit
                    8 -> if (pps == null) pps = unit
                }
            }
            return sps to pps
        }

        fun u16(b: ByteArray, off: Int) = (b[off].toInt() and 0xff) or ((b[off + 1].toInt() and 0xff) shl 8)

        fun u32(b: ByteArray, off: Int): Long =
            (u16(b, off).toLong()) or (u16(b, off + 2).toLong() shl 16)
    }
}
