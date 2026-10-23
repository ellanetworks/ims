package com.ellanetworks.mcptt.platform

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.ParcelFileDescriptor
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress

/** The cellular network the session runs on, and the device's addresses on it. */
data class Cellular(val network: Network, val addresses: List<InetAddress>) {
    /** The device's address of the P-CSCF's family, a global one. */
    fun local(pcscf: InetAddress): InetAddress? = addresses.firstOrNull {
        (it is Inet4Address) == (pcscf is Inet4Address) && !it.isLinkLocalAddress
    }

    /** Binds socket [fd], valid during the call only, to the network. */
    fun bind(fd: Int) = ParcelFileDescriptor.fromFd(fd).use { network.bindSocket(it.fileDescriptor) }
}

/**
 * The cellular network of the app (decision 8: cellular only): the unrestricted one with internet (decision 9: the IMS
 * network is restricted to the system), requested so that it stays up with Wi-Fi the default.
 */
class CellularNetwork(context: Context) {
    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private val state = MutableStateFlow<Cellular?>(null)
    private val links = HashMap<Network, LinkProperties>()

    /** The network, or null without one. */
    val current: StateFlow<Cellular?> = state.asStateFlow()

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = synchronized(links) {
            links[network] = lp
            state.value = Cellular(network, lp.linkAddresses.map { it.address }.filter { it is Inet4Address || it is Inet6Address })
        }

        override fun onLost(network: Network) = synchronized(links) {
            links.remove(network)
            if (state.value?.network == network) state.value = null
        }
    }

    fun start() {
        val request = NetworkRequest.Builder()
            .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_RESTRICTED)
            .build()
        cm.requestNetwork(request, callback)
    }

    fun stop() {
        runCatching { cm.unregisterNetworkCallback(callback) }
        synchronized(links) {
            links.clear()
            state.value = null
        }
    }
}
