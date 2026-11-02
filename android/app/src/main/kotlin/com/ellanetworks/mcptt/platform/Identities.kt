package com.ellanetworks.mcptt.platform

import java.net.InetAddress

// TS 24.229 clause 5.1.1.1A
// TS 31.103 clause 4.2.2
// TS 31.103 clause 4.2.3
// TS 31.103 clause 4.2.4
data class Identities(val impi: String, val impus: List<String>, val homeDomain: String, val pcscfs: List<String> = emptyList(), val isim: Boolean) {
    val impu: String
        get() = impus.first()

    val mcpttImpu: String?
        get() = impus.firstOrNull(::isMcpttIdentity)

    companion object {
        fun isMcpttIdentity(impu: String): Boolean {
            val (scheme, rest) = impu.split(":", limit = 2).takeIf { it.size == 2 } ?: return false
            return (scheme.equals("sip", ignoreCase = true) || scheme.equals("sips", ignoreCase = true)) &&
                rest.startsWith(MCPTT_USER_PREFIX) && rest.indexOf('@') > MCPTT_USER_PREFIX.length
        }

        private const val MCPTT_USER_PREFIX = "mcptt-"

        // TS 23.003 clause 13.2
        // TS 23.003 clause 13.3
        // TS 23.003 clause 13.4B
        fun fromImsi(imsi: String, mncLength: Int): Identities {
            require(imsi.length in 6..15 && imsi.all(Char::isDigit) && mncLength in 2..3) { "IMSI $imsi" }
            val mcc = imsi.substring(0, 3)
            val mnc = imsi.substring(3, 3 + mncLength).padStart(3, '0')
            val domain = "ims.mnc$mnc.mcc$mcc.3gppnetwork.org"
            val impi = "$imsi@$domain"
            return Identities(impi, listOf("sip:$impi"), domain, isim = false)
        }

        // TS 31.103 clause 4.2.8
        fun pcscf(value: ByteArray): String? {
            if (value.isEmpty()) return null
            val addr = value.copyOfRange(1, value.size)
            return when (value[0].toInt()) {
                0x00 -> addr.decodeToString()

                0x01, 0x02 -> when (addr.size) {
                    4, 16 -> InetAddress.getByAddress(addr).hostAddress
                    else -> addr.decodeToString()
                }

                else -> null
            }?.takeIf(String::isNotEmpty)
        }
    }
}
