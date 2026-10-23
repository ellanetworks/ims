package com.ellanetworks.mcptt.call

import com.ellanetworks.mcptt.call.PttController.Source.KEY
import com.ellanetworks.mcptt.call.PttController.Source.SCREEN
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PttControllerTest {
    private val log = mutableListOf<String>()
    private val ended = mutableSetOf<String>()
    private var next = 0
    private var refuse = false
    private val ptt = PttController(
        talk = { g -> if (refuse) null else "c${++next}".also { log += "talk $g $it" } },
        release = { log += "release $it" },
        live = { it !in ended },
    )

    @Test
    fun onePress() {
        assertEquals("c1", ptt.press(KEY, "g"))
        ptt.release(KEY)
        assertEquals(listOf("talk g c1", "release c1"), log)
    }

    // Both down: one press, released by the last up.
    @Test
    fun twoSources() {
        assertEquals("c1", ptt.press(SCREEN, "g"))
        assertEquals("c1", ptt.press(KEY, "g"))
        ptt.release(SCREEN)
        assertEquals(listOf("talk g c1"), log)
        ptt.release(KEY)
        assertEquals(listOf("talk g c1", "release c1"), log)
    }

    // An up of a source not down releases nothing.
    @Test
    fun upNotDown() {
        ptt.press(SCREEN, "g")
        ptt.release(KEY)
        assertEquals(listOf("talk g c1"), log)
    }

    // Refused: nothing held, its up releases nothing.
    @Test
    fun refused() {
        refuse = true
        assertNull(ptt.press(KEY, "g"))
        ptt.release(KEY)
        assertEquals(emptyList<String>(), log)
    }

    // The held call ended: the next down talks anew, the old press forgotten.
    @Test
    fun endedWhileHeld() {
        ptt.press(SCREEN, "g")
        ended += "c1"
        assertEquals("c2", ptt.press(KEY, "g"))
        ptt.release(KEY)
        assertEquals(listOf("talk g c1", "talk g c2", "release c2"), log)
    }
}
