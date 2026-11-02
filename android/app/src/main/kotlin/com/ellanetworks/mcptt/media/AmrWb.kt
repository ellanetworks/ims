package com.ellanetworks.mcptt.media

// RFC 4867 section 5.3
object AmrWb {
    const val SAMPLE_RATE = 16_000
    const val FRAME_SAMPLES = 320

    // TS 26.201 clause 4.1.1 table 1a
    val BIT_RATES = intArrayOf(6_600, 8_850, 12_650, 14_250, 15_850, 18_250, 19_850, 23_050, 23_850)

    // TS 26.201 clause 4.2.2 table 2
    private val BITS = intArrayOf(132, 177, 253, 285, 317, 365, 397, 461, 477, 40)

    const val TYPE_SID = 9
    const val TYPE_SPEECH_LOST = 14
    const val TYPE_NO_DATA = 15

    val NO_DATA = byteArrayOf(0x7c)

    fun type(h: Byte): Int = (h.toInt() shr 3) and 0x0f

    fun length(h: Byte): Int? = when (val t = type(h)) {
        in BITS.indices -> 1 + (BITS[t] + 7) / 8
        TYPE_SPEECH_LOST, TYPE_NO_DATA -> 1
        else -> null
    }

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
