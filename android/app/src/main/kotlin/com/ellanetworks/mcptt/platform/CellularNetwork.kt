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

data class Cellular(val network: Network, val addresses: List<InetAddress>) {
    fun local(pcscf: InetAddress): InetAddress? = addresses.firstOrNull {
        (it is Inet4Address) == (pcscf is Inet4Address) && !it.isLinkLocalAddress
    }

    fun bind(fd: Int) = ParcelFileDescriptor.fromFd(fd).use { network.bindSocket(it.fileDescriptor) }
}

class CellularNetwork(context: Context) {
    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private val state = MutableStateFlow<Cellular?>(null)
    private val links = HashMap<Network, LinkProperties>()

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
