package com.ellanetworks.mcptt.platform

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AccessNetworkInfoTest {
    // TS 24.229 §7.2A.4.3 item 4, its example.
    @Test
    fun lte() {
        assertEquals(
            "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=1112233C476B4321",
            accessNetworkInfo(Cell.Lte("111", "22", 0x33C4, 0x76B4321, tdd = false)),
        )
        assertEquals(
            "3GPP-E-UTRAN-TDD;utran-cell-id-3gpp=001010001100000A",
            accessNetworkInfo(Cell.Lte("001", "01", 1, 0x100000A, tdd = true)),
        )
        assertEquals(
            "3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100000100000AB",
            accessNetworkInfo(Cell.Lte("001", "01", 1, 0xAB, tdd = false), fiveGc = true),
        )
    }

    // Item 11: TAC of 6, NCI of 9.
    @Test
    fun nr() {
        assertEquals(
            "3GPP-NR-TDD;utran-cell-id-3gpp=001010000010000000AB",
            accessNetworkInfo(Cell.Nr("001", "01", 1, 0xAB, tdd = true)),
        )
    }

    // The cell's identity unknown, of a device without location access: the access type alone.
    @Test
    fun unknown() {
        assertEquals("3GPP-E-UTRAN-FDD", accessNetworkInfo(Cell.Lte(null, null, null, null, tdd = false)))
        assertEquals("3GPP-E-UTRAN-FDD", accessNetworkInfo(Cell.Lte("001", "01", null, 5, tdd = false)))
        assertEquals("3GPP-NR-FDD", accessNetworkInfo(Cell.Nr("001", "1", 1, 1, tdd = false)))
    }

    @Test
    fun duplex() {
        assertFalse(lteTdd(3))
        assertTrue(lteTdd(41))
        assertTrue(nrTdd(78))
        assertFalse(nrTdd(1))
        assertTrue(nrTdd(260))
    }
}
