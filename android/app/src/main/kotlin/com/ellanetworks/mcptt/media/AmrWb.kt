package com.ellanetworks.mcptt.media

/**
 * AMR-WB frames in the storage format of RFC 4867 §5.3, the session's: a header octet (frame type in bits 6–3, the
 * quality bit 2) and the frame's speech bits, padded to an octet.
 */
object AmrWb {
    /** The sampling rate, and the samples of a frame of 20 ms. */
    const val SAMPLE_RATE = 16_000
    const val FRAME_SAMPLES = 320

    /** The bit rates of the modes 0 to 8 (TS 26.201 Table 1a), the encoder's bit rate of each. */
    val BIT_RATES = intArrayOf(6_600, 8_850, 12_650, 14_250, 15_850, 18_250, 19_850, 23_050, 23_850)

    /** The speech bits of frame types 0 to 9, 9 SID (TS 26.201 Table 2). */
    private val BITS = intArrayOf(132, 177, 253, 285, 317, 365, 397, 461, 477, 40)

    const val TYPE_SID = 9
    const val TYPE_SPEECH_LOST = 14
    const val TYPE_NO_DATA = 15

    /** A NO_DATA frame, its quality bit set. */
    val NO_DATA = byteArrayOf(0x7c)

    /** The type of the frame of header [h]. */
    fun type(h: Byte): Int = (h.toInt() shr 3) and 0x0f

    /** The length of the frame of header [h], its header counted; null for a type reserved (10 to 13). */
    fun length(h: Byte): Int? = when (val t = type(h)) {
        in BITS.indices -> 1 + (BITS[t] + 7) / 8
        TYPE_SPEECH_LOST, TYPE_NO_DATA -> 1
        else -> null
    }

    /** The frames of [b], frames one after the other: null if the last is short or a type is reserved. */
    fun split(b: ByteArray, size: Int = b.size): List<ByteArray>? {
        val out = ArrayList<ByteArray>(1)
        var i = 0
        while (i < size) {
            val n = length(b[i]) ?: return null
            if (i + n > size) return null
            out.add(b.copyOfRange(i, i + n))
            i += n
        }
        return out
    }
}
