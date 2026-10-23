package com.ellanetworks.mcptt.platform

import android.content.Context
import android.net.IpSecAlgorithm
import android.net.IpSecManager
import android.net.IpSecManager.SecurityParameterIndex
import android.net.IpSecTransform
import android.os.ParcelFileDescriptor
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.IOException
import java.net.InetAddress

/** An endpoint of an SA set (the Go package mobile's saEndpointJSON). */
@Serializable
data class SaEndpoint(val addr: String, val portC: Int, val portS: Int, val spiC: Long, val spiS: Long)

/** The SA set of Platform.InstallSA (TS 33.203 §7.1), its keys those of the SAs (Annex I), in hex. */
@Serializable
data class SaSet(
    val id: String,
    val local: SaEndpoint,
    val remote: SaEndpoint,
    val integrity: String,
    val encryption: String,
    val ik: String,
    val ck: String,
) {
    companion object {
        private val json = Json { ignoreUnknownKeys = true }

        fun decode(text: String): SaSet = json.decodeFromString(text)
    }
}

/**
 * The SAs of the UE's registrations as IpSecManager's transport-mode transforms (TS 33.203 §7.1): the inbound SPIs
 * reserved for the UE's Security-Client, each set's four SAs, and the transforms of a socket on a protected port.
 * IpSecManager applies one transform a direction to a socket, the latest: port_us, the same across sets (§7.4 NOTE
 * 10), carries the newest set's only.
 */
class Ipsec(private val context: Context) {
    private val ism = context.getSystemService(IpSecManager::class.java)

    private val spis = HashMap<Long, SecurityParameterIndex>()
    private val sets = HashMap<String, Installed>()

    /** The four SAs of a set: by the UE's port, the transforms of its socket out and in. */
    private class Installed(
        val portC: Int,
        val portS: Int,
        val outS: IpSecTransform,
        val inC: IpSecTransform,
        val inS: IpSecTransform,
        val outC: IpSecTransform,
        val owned: List<AutoCloseable>,
    )

    /** Reserves an inbound SPI to the device's address [local], for the Security-Client (§7.2). */
    @Synchronized
    fun allocateSpi(local: String): Long {
        val spi = ism.allocateSecurityParameterIndex(InetAddress.getByName(local))
        val v = spi.spi.toLong() and 0xffffffffL
        spis[v] = spi
        return v
    }

    /** Releases an SPI of allocateSpi, its SAs removed or never installed. */
    @Synchronized
    fun releaseSpi(spi: Long) {
        spis.remove(spi)?.close()
    }

    /** Builds the four SAs of a set (§7.1), the inbound SPIs those reserved, the outbound the P-CSCF's. */
    @Synchronized
    fun install(set: SaSet) {
        require(set.id !in sets) { "SA set ${set.id} installed" }
        val local = InetAddress.getByName(set.local.addr)
        val remote = InetAddress.getByName(set.remote.addr)
        val inSpiC = requireNotNull(spis[set.local.spiC]) { "SPI ${set.local.spiC} not reserved" }
        val inSpiS = requireNotNull(spis[set.local.spiS]) { "SPI ${set.local.spiS} not reserved" }

        val owned = mutableListOf<AutoCloseable>()
        try {
            val outSpiS = ism.allocateSecurityParameterIndex(remote, set.remote.spiS.toInt()).also { owned.add(it) }
            val outSpiC = ism.allocateSecurityParameterIndex(remote, set.remote.spiC.toInt()).also { owned.add(it) }
            val ik = set.ik.hexToByteArrayOrNull() ?: error("IK")
            val ck = set.ck.hexToByteArrayOrNull() ?: error("CK")
            fun transform(source: InetAddress, spi: SecurityParameterIndex) =
                builder(set.integrity, set.encryption, ik, ck).buildTransportModeTransform(source, spi).also { owned.add(it) }

            sets[set.id] = Installed(
                portC = set.local.portC,
                portS = set.local.portS,
                // UE port_c to P-CSCF port_s, and back; UE port_s to P-CSCF port_c, and back.
                outS = transform(local, outSpiS),
                inC = transform(remote, inSpiC),
                inS = transform(remote, inSpiS),
                outC = transform(local, outSpiC),
                owned = owned,
            )
        } catch (e: Exception) {
            owned.asReversed().forEach { runCatching(it::close) }
            throw e
        }
    }

    /** Applies the SAs of set [id] to socket [fd], valid during the call only, bound to the UE's [port] of the set. */
    @Synchronized
    fun protect(id: String, fd: Int, port: Int) {
        val s = requireNotNull(sets[id]) { "SA set $id not installed" }
        val (out, inbound) = when (port) {
            s.portC -> s.outS to s.inC
            s.portS -> s.outC to s.inS
            else -> throw IllegalArgumentException("port $port not of SA set $id")
        }
        // A duplicate is the same socket: its transforms the socket's.
        ParcelFileDescriptor.fromFd(fd).use { dup ->
            ism.applyTransportModeTransform(dup.fileDescriptor, IpSecManager.DIRECTION_OUT, out)
            ism.applyTransportModeTransform(dup.fileDescriptor, IpSecManager.DIRECTION_IN, inbound)
        }
    }

    /** Removes the SAs of set [id]; its inbound SPIs stay reserved until released. */
    @Synchronized
    fun remove(id: String) {
        val s = sets.remove(id) ?: return
        var failed: Exception? = null
        for (c in s.owned.asReversed()) {
            try {
                c.close()
            } catch (e: IOException) {
                failed = failed ?: e
            }
        }
        failed?.let { throw it }
    }

    private fun builder(integrity: String, encryption: String, ik: ByteArray, ck: ByteArray): IpSecTransform.Builder {
        val b = IpSecTransform.Builder(context)
        b.setAuthentication(
            when (integrity) {
                // IK extended to 160 bits by the session (Annex I).
                "hmac-sha-1-96" -> IpSecAlgorithm(IpSecAlgorithm.AUTH_HMAC_SHA1, ik, TRUNCATION_BITS)

                "hmac-md5-96" -> IpSecAlgorithm(IpSecAlgorithm.AUTH_HMAC_MD5, ik, TRUNCATION_BITS)

                else -> throw IllegalArgumentException("integrity $integrity")
            },
        )
        when (encryption) {
            "aes-cbc" -> b.setEncryption(IpSecAlgorithm(IpSecAlgorithm.CRYPT_AES_CBC, ck))

            // No encryption set: ESP with the null cipher.
            "null" -> Unit

            else -> throw IllegalArgumentException("encryption $encryption")
        }
        return b
    }

    private companion object {
        const val TRUNCATION_BITS = 96
    }
}
