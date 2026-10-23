package com.ellanetworks.mcptt.platform

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ApduTest {
    @Test
    fun responses() {
        val r = Response.parse("80036162639000")!!
        assertEquals(0x9000, r.sw)
        assertArrayEquals(byteArrayOf(0x80.toByte(), 3, 0x61, 0x62, 0x63), r.data)
        assertEquals(0x6a82, Response.parse("6A82")!!.sw)
        for (bad in listOf(null, "", "90", "9000F", "zz00")) assertNull(bad, Response.parse(bad))
    }

    // TS 31.103 §4.2.2: EF_IMPI, an NAI TLV of tag '80', padded with 'FF'.
    @Test
    fun tlvs() {
        val impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org"
        val ef = byteArrayOf(0x80.toByte(), impi.length.toByte()) + impi.encodeToByteArray() + ByteArray(4) { -1 }
        assertEquals(impi, tlv(ef, 0x80)?.decodeToString())

        val long = "sip:" + "a".repeat(200) + "@example.org"
        val ef2 = byteArrayOf(0x80.toByte(), 0x81.toByte(), long.length.toByte()) + long.encodeToByteArray()
        assertEquals(long, tlv(ef2, 0x80)?.decodeToString())

        assertNull(tlv(byteArrayOf(0x81.toByte(), 1, 0), 0x80))
        assertNull(tlv(byteArrayOf(0x80.toByte(), 5, 0), 0x80))
        assertNull(tlv(ByteArray(8) { -1 }, 0x80))
    }

    // ETSI TS 102 221 §11.1.1.4.3: the file descriptor of a linear fixed EF, its record length and count.
    @Test
    fun records() {
        val fcp = "621e8205422100400583026f04a5038001718a01058b036f0602800200808800".hexToByteArrayOrNull()!!
        assertEquals(5, recordCount(fcp))
        assertNull(recordCount("62038201".hexToByteArrayOrNull()!!))
    }
}
