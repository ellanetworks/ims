package com.ellanetworks.mcptt

import org.junit.Assert.assertEquals
import org.junit.Test

class StatusTest {
    @Test
    fun offlineShowsNoNetwork() {
        assertEquals(Status.NoNetwork, shown(Status.Registered("sip:a@b"), online = false))
        assertEquals(Status.NoNetwork, shown(Status.Failed("timeout"), online = false))
        assertEquals(Status.NoNetwork, shown(Status.Registering, online = false))
    }

    @Test
    fun onlineShowsRegistration() {
        assertEquals(Status.Registered("sip:a@b"), shown(Status.Registered("sip:a@b"), online = true))
    }

    @Test
    fun rejectedShownRegardless() {
        assertEquals(Status.Rejected, shown(Status.Rejected, online = false))
    }
}
