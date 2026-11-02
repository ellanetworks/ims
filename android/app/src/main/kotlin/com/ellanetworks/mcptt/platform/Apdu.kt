package com.ellanetworks.mcptt.platform

// ISO/IEC 7816-4 clause 5.1
class Response(val data: ByteArray, val sw: Int) {
    val ok: Boolean
        get() = sw == SW_OK

    companion object {
        const val SW_OK = 0x9000

        fun parse(hex: String?): Response? {
            if (hex == null || hex.length < 4 || hex.length % 2 != 0) return null
            val b = hex.hexToByteArrayOrNull() ?: return null
            val sw = (b[b.size - 2].toInt() and 0xff shl 8) or (b[b.size - 1].toInt() and 0xff)
            return Response(b.copyOfRange(0, b.size - 2), sw)
        }
    }
}

// ISO/IEC 7816-4 clause 5.2
// TS 31.103 clause 4.2
fun tlv(b: ByteArray, tag: Int): ByteArray? {
    if (b.size < 2 || (b[0].toInt() and 0xff) != tag) return null
    var off = 2
    var len = b[1].toInt() and 0xff
    if (len == 0x81) {
        if (b.size < 3) return null
        len = b[2].toInt() and 0xff
        off = 3
    } else if (len > 0x7f) {
        return null
    }
    if (off + len > b.size) return null
    return b.copyOfRange(off, off + len)
}

// ETSI TS 102 221 clause 11.1.1.4.3
fun recordCount(fcp: ByteArray): Int? {
    val t = tlv(fcp, 0x62) ?: return null
    var i = 0
    while (i + 1 < t.size) {
        val tag = t[i].toInt() and 0xff
        val len = t[i + 1].toInt() and 0xff
        if (i + 2 + len > t.size) return null
        if (tag == 0x82 && len >= 5) return t[i + 2 + 4].toInt() and 0xff
        i += 2 + len
    }
    return null
}

internal fun String.hexToByteArrayOrNull(): ByteArray? =
    if (length % 2 != 0) null else runCatching { chunked(2).map { it.toInt(16).toByte() }.toByteArray() }.getOrNull()

internal fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
