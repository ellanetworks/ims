package com.ellanetworks.mcptt.platform

import com.ellanetworks.mcptt.go.mobile.Platform
import java.io.IOException

/**
 * The platform of a session (the Go package mobile's Platform): the network its sockets are bound to, the UICC for
 * AKA, IpSecManager for its SAs, the alarms and wake locks of the device, and where its events and log go. The
 * session calls it from its goroutines: each method is thread-safe and returns promptly.
 */
class AndroidPlatform(
    private val network: () -> Cellular?,
    private val isim: Isim,
    private val hasIsim: Boolean,
    private val ipsec: Ipsec,
    private val alarms: Alarms,
    private val onEvent: (String) -> Unit,
    private val onLog: (Int, String) -> Unit,
) : Platform {
    override fun bindSocket(fd: Long) {
        val n = network() ?: throw IOException("no cellular network")
        n.bind(fd.toInt())
    }

    override fun authenticate(rand: ByteArray, autn: ByteArray): ByteArray = isim.authenticate(hasIsim, rand, autn)

    override fun allocateSPI(local: String): Long = ipsec.allocateSpi(local)

    override fun releaseSPI(spi: Long) = ipsec.releaseSpi(spi)

    override fun installSA(set: String) = ipsec.install(SaSet.decode(set))

    override fun protectSocket(id: String, fd: Long, port: Long) = ipsec.protect(id, fd.toInt(), port.toInt())

    override fun removeSA(id: String) = ipsec.remove(id)

    override fun wake(id: Long, ms: Long) = alarms.wake(id, ms)

    override fun cancelWake(id: Long) = alarms.cancel(id)

    override fun keepAwake(ms: Long) = alarms.keepAwake(ms)

    override fun event(event: String) = onEvent(event)

    override fun log(level: Long, line: String) = onLog(level.toInt(), line)
}
