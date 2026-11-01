package io.github.aleynikovich.remoter

import org.json.JSONObject
import kotlin.math.abs
import kotlin.math.ceil

/**
 * Virtual monitors: the host can show only part of a display, so a wide
 * monitor can be watched as pieces shaped like this screen ("fit"), as
 * halves or thirds. Mirrors internal/viewer/views.go.
 */
object Views {
    val splits = listOf("fit", "halves", "thirds", "off")

    fun splitLabel(split: String) = when (split) {
        "halves" -> "halves"
        "thirds" -> "thirds"
        "off" -> "off"
        else -> "fit my screen"
    }

    /** A part of a display, as fractions 0..1 of its width and height. */
    data class Region(val x: Double, val y: Double, val w: Double, val h: Double) {
        fun toJson(): JSONObject = JSONObject().put("x", x).put("y", y).put("w", w).put("h", h)

        fun same(o: Region?): Boolean = o != null &&
            abs(x - o.x) < 0.002 && abs(y - o.y) < 0.002 && abs(w - o.w) < 0.002 && abs(h - o.h) < 0.002

        companion object {
            /** The region in a message, or null for the whole display. */
            fun from(o: JSONObject?): Region? {
                if (o == null) return null
                val r = Region(o.optDouble("x", 0.0), o.optDouble("y", 0.0), o.optDouble("w", 1.0), o.optDouble("h", 1.0))
                return if (r.x < 0.001 && r.y < 0.001 && r.w > 0.999 && r.h > 0.999) null else r
            }
        }
    }

    data class View(val display: Int, val region: Region?, val label: String)

    /** Cuts a w×h display along its long side; see Parts in views.go. */
    fun parts(w: Int, h: Int, viewAspect: Double, split: String): List<Region> {
        if (w <= 0 || h <= 0) return emptyList()
        val aspect = w.toDouble() / h
        val wide = if (viewAspect > 0) aspect >= viewAspect else aspect >= 1
        val n: Int
        val size: Double
        when (split) {
            "halves", "thirds" -> {
                n = if (split == "thirds") 3 else 2
                size = 1.0 / n
            }
            "off" -> return emptyList()
            else -> {
                if (viewAspect <= 0) return emptyList()
                val r = if (wide) aspect / viewAspect else viewAspect / aspect
                if (r < 1.3) return emptyList()
                n = ceil(r - 0.15).toInt()
                size = 1 / r
            }
        }
        return List(n) { i ->
            val start = if (n > 1) i * (1 - size) / (n - 1) else 0.0
            if (wide) Region(start, 0.0, size, 1.0) else Region(0.0, start, 1.0, size)
        }
    }

    fun partName(i: Int, n: Int, wide: Boolean): String {
        val names = if (wide) mapOf(2 to listOf("Left", "Right"), 3 to listOf("Left", "Centre", "Right"))
        else mapOf(2 to listOf("Top", "Bottom"), 3 to listOf("Top", "Middle", "Bottom"))
        return names[n]?.get(i) ?: "Part ${i + 1}"
    }

    /** Each display followed by its virtual monitors, then every display at once. */
    fun list(count: Int, all: Boolean, sizes: List<Pair<Int, Int>>, viewAspect: Double, split: String): List<View> {
        val out = mutableListOf<View>()
        val n = if (all && count > 1) count + 1 else count
        for (d in 0 until n) {
            val name = when {
                count == 1 -> "Whole screen"
                d < count -> "Monitor ${d + 1}"
                else -> "All monitors"
            }
            out += View(d, null, name)
            val s = sizes.getOrNull(d) ?: continue
            val ps = parts(s.first, s.second, viewAspect, split)
            val wide = ps.isNotEmpty() && ps[0].h == 1.0
            val prefix = if (count > 1) "$name · " else ""
            ps.forEachIndexed { i, r -> out += View(d, r, prefix + partName(i, ps.size, wide)) }
        }
        return out
    }
}
