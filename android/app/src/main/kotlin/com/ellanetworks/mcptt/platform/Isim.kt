package com.ellanetworks.mcptt.platform

import android.annotation.SuppressLint
import android.telephony.IccOpenLogicalChannelResponse
import android.telephony.TelephonyManager
import android.util.Base64

/**
 * The UICC of the subscription [tm] is for: the ISIM's identities and IMS AKA (TS 33.203 §6.1), the USIM's without
 * an ISIM (TS 24.229 §5.1.1.1A). All of it needs the app's carrier privileges (the SIM's ARA-M rule); TelephonyManager's
 * ISIM getters are system APIs, so the identities are read over a logical channel.
 */
// The IMSI and the IMEI need READ_PRIVILEGED_PHONE_STATE, or the carrier privileges the app has: lint knows of the first.
@SuppressLint("MissingPermission", "HardwareIds")
class Isim(private val tm: TelephonyManager) {
    /** The device's IMEI, of its instance ID (TS 24.229 §5.1.1.2.1, the one VoLTE registers with). */
    fun imei(): String = tm.imei.orEmpty()

    /** The identities: the ISIM's, or the IMSI's without an ISIM. */
    fun identities(): Identities = readIsim() ?: run {
        val imsi = requireNotNull(tm.subscriberId) { "no IMSI: no carrier privileges?" }
        // MCC and MNC of the USIM's EF_AD (TS 31.102 §4.2.18), as the framework reads them.
        val mncLength = tm.simOperator.length - 3
        Identities.fromImsi(imsi, mncLength)
    }

    /**
     * Runs an AKA challenge (TS 31.103 §7.1.2.1, TS 31.102 §7.1.2.1) on the ISIM, the USIM without one: the data of the
     * AUTHENTICATE response (DB… or DC…), or its status word. The RILs give a failed AUTHENTICATE as null, or as its
     * status word (Samsung, 98 62): both are a MAC failure, the only failure that leaves the card without data.
     */
    fun authenticate(isim: Boolean, rand: ByteArray, autn: ByteArray): ByteArray {
        val challenge = byteArrayOf(rand.size.toByte()) + rand + byteArrayOf(autn.size.toByte()) + autn
        val app = if (isim) TelephonyManager.APPTYPE_ISIM else TelephonyManager.APPTYPE_USIM
        val out = tm.getIccAuthentication(app, TelephonyManager.AUTHTYPE_EAP_AKA, Base64.encodeToString(challenge, Base64.NO_WRAP))
            ?: return SW_MAC_FAILURE
        return Base64.decode(out, Base64.DEFAULT).takeIf(ByteArray::isNotEmpty) ?: SW_MAC_FAILURE
    }

    private fun readIsim(): Identities? {
        val ch = tm.iccOpenLogicalChannel(ISIM_AID, P2_FCP)
        if (ch.status != IccOpenLogicalChannelResponse.STATUS_NO_ERROR) return null
        try {
            val c = ch.channel
            val impi = binary(c, EF_IMPI)?.let { tlv(it, TAG)?.decodeToString() } ?: return null
            val domain = binary(c, EF_DOMAIN)?.let { tlv(it, TAG)?.decodeToString() } ?: return null
            val impus = records(c, EF_IMPU).mapNotNull { tlv(it, TAG)?.decodeToString()?.takeIf(String::isNotEmpty) }
            if (impus.isEmpty()) return null
            val pcscfs = records(c, EF_PCSCF).mapNotNull { r -> tlv(r, TAG)?.let(Identities::pcscf) }
            return Identities(impi, impus, domain, pcscfs, isim = true)
        } finally {
            tm.iccCloseLogicalChannel(ch.channel)
        }
    }

    private fun select(c: Int, fid: String): Response? =
        Response.parse(tm.iccTransmitApduLogicalChannel(c, 0x00, INS_SELECT, 0x00, P2_FCP, 2, fid))?.takeIf { it.ok }

    private fun binary(c: Int, fid: String): ByteArray? {
        select(c, fid) ?: return null
        return Response.parse(tm.iccTransmitApduLogicalChannel(c, 0x00, INS_READ_BINARY, 0, 0, 0, ""))
            ?.takeIf { it.ok }?.data
    }

    private fun records(c: Int, fid: String): List<ByteArray> {
        val fcp = select(c, fid) ?: return emptyList()
        val n = recordCount(fcp.data) ?: MAX_RECORDS
        return (1..minOf(n, MAX_RECORDS)).asSequence()
            .map { Response.parse(tm.iccTransmitApduLogicalChannel(c, 0x00, INS_READ_RECORD, it, P2_ABSOLUTE, 0, "")) }
            .takeWhile { it != null && it.ok }
            .map { it!!.data }
            .toList()
    }

    companion object {
        /** The ISIM's AID, its RID and application code (ETSI TS 101 220 Annex E): selected by partial AID. */
        private const val ISIM_AID = "A0000000871004"

        private const val EF_IMPI = "6F02"
        private const val EF_DOMAIN = "6F03"
        private const val EF_IMPU = "6F04"
        private const val EF_PCSCF = "6F09"
        private const val TAG = 0x80

        private const val INS_SELECT = 0xA4
        private const val INS_READ_BINARY = 0xB0
        private const val INS_READ_RECORD = 0xB2
        private const val P2_FCP = 0x04
        private const val P2_ABSOLUTE = 0x04
        private const val MAX_RECORDS = 16

        /** The status word of an AUTHENTICATE whose MAC the card found wrong (TS 31.102 §7.1.2.1). */
        val SW_MAC_FAILURE = byteArrayOf(0x98.toByte(), 0x62)
    }
}
