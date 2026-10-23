package com.ellanetworks.mcptt

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class HostPortTest {
    @Test
    fun parses() {
        assertEquals("10.206.0.2" to 5060, hostPort("10.206.0.2"))
        assertEquals("10.206.0.2" to 5062, hostPort(" 10.206.0.2:5062 "))
        assertEquals("pcscf.example" to 5060, hostPort("pcscf.example"))
        assertEquals("2001:db8::1" to 5060, hostPort("2001:db8::1"))
        assertEquals("2001:db8::1" to 5060, hostPort("[2001:db8::1]"))
        assertEquals("2001:db8::1" to 5070, hostPort("[2001:db8::1]:5070"))
        for (bad in listOf("", ":5060", "host:", "host:x", "host:70000")) assertNull(bad, hostPort(bad))
    }
}
