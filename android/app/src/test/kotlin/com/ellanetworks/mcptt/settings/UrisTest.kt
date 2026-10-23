package com.ellanetworks.mcptt.settings

import org.junit.Assert.assertEquals
import org.junit.Test

class UrisTest {
    @Test
    fun mcpttId() {
        assertEquals("sip:moto@mcptt.example", sipUri(" moto@mcptt.example "))
        assertEquals("sip:moto@mcptt.example", sipUri("sip:moto@mcptt.example"))
        assertEquals("sips:moto@mcptt.example", sipUri("sips:moto@mcptt.example"))
        assertEquals("", sipUri(" "))
    }

    @Test
    fun group() {
        val id = "sip:moto@mcptt.example"
        assertEquals("sip:lab@mcptt.example", groupUri("lab", id))
        assertEquals("sip:fire@other.example", groupUri("fire@other.example", id))
        assertEquals("sip:fire@other.example", groupUri("sip:fire@other.example", id))
        // No MCPTT ID yet: the name as typed, its domain missing.
        assertEquals("sip:lab", groupUri("lab", ""))
    }

    @Test
    fun shownWithoutScheme() {
        assertEquals("moto@mcptt.example", shown("sip:moto@mcptt.example"))
        assertEquals("mcptt.example", domainOf("sip:moto@mcptt.example;user=phone"))
    }
}
