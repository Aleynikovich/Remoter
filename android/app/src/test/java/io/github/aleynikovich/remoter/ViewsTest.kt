package io.github.aleynikovich.remoter

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ViewsTest {
    @Test
    fun ultrawideSplitsIntoThreeScreenShapedPieces() {
        val ps = Views.parts(5120, 1440, 1.6, "fit")
        assertEquals(3, ps.size)
        ps.forEach { assertEquals(2304.0, it.w * 5120, 1.0) }
        assertEquals(0.0, ps[0].x, 1e-9)
        assertEquals(1.0, ps[2].x + ps[2].w, 1e-9)
    }

    @Test
    fun phoneSeesTwoPieces() {
        assertEquals(2, Views.parts(5120, 1440, 20.0 / 9, "fit").size)
        assertTrue(Views.parts(1920, 1080, 1.6, "fit").isEmpty())
        assertEquals(3, Views.parts(1920, 1080, 1.6, "thirds").size)
    }

    @Test
    fun listsViews() {
        val vs = Views.list(2, true, listOf(5120 to 1440, 3840 to 2160, 8960 to 2160), 1.6, "fit")
        assertEquals(
            listOf("Monitor 1", "Monitor 1 · Left", "Monitor 1 · Centre", "Monitor 1 · Right", "Monitor 2",
                "All monitors", "All monitors · Left", "All monitors · Centre", "All monitors · Right"),
            vs.map { it.label },
        )
    }
}
