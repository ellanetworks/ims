package com.ellanetworks.mcptt.platform

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class IdentitiesTest {
    // TS 23.003 §13.2–13.4B, its examples.
    @Test
    fun fromImsi() {
        val ids = Identities.fromImsi("234150999999999", 2)
        assertEquals("234150999999999@ims.mnc015.mcc234.3gppnetwork.org", ids.impi)
        assertEquals("sip:234150999999999@ims.mnc015.mcc234.3gppnetwork.org", ids.impu)
        assertEquals("ims.mnc015.mcc234.3gppnetwork.org", ids.homeDomain)
        assertEquals(false, ids.isim)
        assertEquals("ims.mnc123.mcc310.3gppnetwork.org", Identities.fromImsi("310123000000001", 3).homeDomain)
    }

    // The MCPTT identity is the EF_IMPU record sip:mcptt-<MSISDN>@<domain>, whatever its place; without one, none.
    @Test
    fun mcpttImpu() {
        val domain = "ims.mnc001.mcc001.3gppnetwork.org"
        val ids = Identities("001010000000001@$domain", listOf("sip:+15550000001@$domain", "tel:+15550000001", "sip:mcptt-15550000001@$domain"), domain, isim = true)
        assertEquals("sip:+15550000001@$domain", ids.impu)
        assertEquals("sip:mcptt-15550000001@$domain", ids.mcpttImpu)
        assertNull(Identities("001010000000001@$domain", listOf("sip:+15550000001@$domain", "sip:mcptt-@$domain", "tel:mcptt-1"), domain, isim = true).mcpttImpu)
        assertNull(Identities.fromImsi("001010000000001", 2).mcpttImpu)
    }

    // TS 31.103 §4.2.8: an FQDN, IPv4 or IPv6, as text or octets.
    @Test
    fun pcscfs() {
        assertEquals("pcscf.ims.example", Identities.pcscf(byteArrayOf(0) + "pcscf.ims.example".encodeToByteArray()))
        assertEquals("10.206.0.2", Identities.pcscf(byteArrayOf(1, 10, (206).toByte(), 0, 2)))
        assertEquals("10.206.0.2", Identities.pcscf(byteArrayOf(1) + "10.206.0.2".encodeToByteArray()))
        assertEquals("2001:db8:0:0:0:0:0:1", Identities.pcscf(byteArrayOf(2, 0x20, 0x01, 0x0d, 0xb8.toByte()) + ByteArray(11) + byteArrayOf(1)))
        assertNull(Identities.pcscf(byteArrayOf(7, 1)))
        assertNull(Identities.pcscf(byteArrayOf()))
    }
}
