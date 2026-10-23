package com.ellanetworks.mcptt.service

import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.call.CallInfo.State
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ShownTest {
    private fun call(id: String, state: State) = CallInfo(id, "sip:g@x", null, state)

    @Test
    fun ringingFirstThenActiveThenHeld() {
        val held = call("h", State.HELD)
        val active = call("a", State.ACTIVE)
        val ringing = call("r", State.RINGING)
        assertEquals("r", shown(Status.Stopped, listOf(ringing, active, held), null).call?.id)
        assertEquals("a", shown(Status.Stopped, listOf(active, held), null).call?.id)
        assertEquals("a", shown(Status.Stopped, listOf(held, active), null).call?.id)
        assertEquals("h", shown(Status.Stopped, listOf(held), null).call?.id)
        assertNull(shown(Status.Stopped, emptyList(), null).call)
    }

    @Test
    fun routeAndTalkTimeLeaveItAlike() {
        val c = call("a", State.ACTIVE)
        val moved = c.copy(endpoint = 2, routes = listOf(1, 2), talkUntil = 10, talkFor = 30, refused = 5)
        assertEquals(shown(Status.Stopped, listOf(c), null), shown(Status.Stopped, listOf(moved), null))
        val talking = c.copy(transmitting = true, talker = "sip:me@x")
        assertEquals(talking, shown(Status.Stopped, listOf(talking), null).call)
    }
}
