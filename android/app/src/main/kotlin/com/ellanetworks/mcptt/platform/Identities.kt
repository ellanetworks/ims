package com.ellanetworks.mcptt.platform

import java.net.InetAddress

/**
 * The user's IMS identities (TS 24.229 §5.1.1.1A): of the ISIM (TS 31.103 §4.2.2–4.2.4), or derived from the IMSI
 * without one (TS 23.003 §13.2–13.4B). [pcscfs] are the ISIM's P-CSCF addresses (§4.2.8), the first the preferred.
 */
data class Identities(val impi: String, val impus: List<String>, val homeDomain: String, val pcscfs: List<String> = emptyList(), val isim: Boolean) {
    /** The default public user identity: the first of EF_IMPU (TS 31.103 §4.2.4). */
    val impu: String
        get() = impus.first()

    /**
     * The MCPTT public user identity, sip:mcptt-<MSISDN>@<domain>: a record of EF_IMPU after the default one, of an
     * implicit registration set of its own, which the app registers beside the phone's IMS client (TS 24.229 §3.1).
     */
    val mcpttImpu: String?
        get() = impus.firstOrNull(::isMcpttIdentity)

    companion object {
        fun isMcpttIdentity(impu: String): Boolean {
            val (scheme, rest) = impu.split(":", limit = 2).takeIf { it.size == 2 } ?: return false
            return (scheme.equals("sip", ignoreCase = true) || scheme.equals("sips", ignoreCase = true)) &&
                rest.startsWith(MCPTT_USER_PREFIX) && rest.indexOf('@') > MCPTT_USER_PREFIX.length
        }

        private const val MCPTT_USER_PREFIX = "mcptt-"

        /** The identities of the IMSI, of its MNC's length (TS 23.003 §13.2, §13.3, §13.4B). */
        fun fromImsi(imsi: String, mncLength: Int): Identities {
            require(imsi.length in 6..15 && imsi.all(Char::isDigit) && mncLength in 2..3) { "IMSI $imsi" }
            val mcc = imsi.substring(0, 3)
            val mnc = imsi.substring(3, 3 + mncLength).padStart(3, '0')
            val domain = "ims.mnc$mnc.mcc$mcc.3gppnetwork.org"
            val impi = "$imsi@$domain"
            return Identities(impi, listOf("sip:$impi"), domain, isim = false)
        }

        /**
         * A P-CSCF address of EF_P-CSCF (TS 31.103 §4.2.8): its type '00' an FQDN, '01' IPv4, '02' IPv6; IPv4 and IPv6
         * either as text or as their octets.
         */
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
