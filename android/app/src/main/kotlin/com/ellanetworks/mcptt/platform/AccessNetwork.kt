package com.ellanetworks.mcptt.platform

import android.content.Context
import android.telephony.AccessNetworkConstants
import android.telephony.CellIdentity
import android.telephony.CellIdentityLte
import android.telephony.CellIdentityNr
import android.telephony.NetworkRegistrationInfo
import android.telephony.ServiceState
import android.telephony.TelephonyCallback
import android.telephony.TelephonyManager
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.util.concurrent.Executors

/**
 * The access network info of the cell the device is on, for the session's P-Access-Network-Info (TS 24.229
 * §7.2A.4): of the packet-switched registration's cell, as ServiceState gives it. The system gives the cell's identity
 * to an app with location access only: without, the access type alone.
 */
class AccessNetwork(context: Context, private val tm: TelephonyManager) {
    private val executor = Executors.newSingleThreadExecutor()
    private val state = MutableStateFlow(DEFAULT)

    /** The access network info now. */
    val current: StateFlow<String> = state.asStateFlow()

    private val callback = object : TelephonyCallback(), TelephonyCallback.ServiceStateListener {
        override fun onServiceStateChanged(ss: ServiceState) {
            state.value = info(ss) ?: state.value
        }
    }

    fun start() {
        tm.registerTelephonyCallback(executor, callback)
    }

    /** Unregisters the callback, if registered: a run that failed before its start stops too. */
    fun stop() {
        runCatching { tm.unregisterTelephonyCallback(callback) }
    }

    private fun info(ss: ServiceState): String? {
        val reg = ss.networkRegistrationInfoList.firstOrNull {
            it.domain and NetworkRegistrationInfo.DOMAIN_PS != 0 &&
                it.transportType == AccessNetworkConstants.TRANSPORT_TYPE_WWAN && it.isNetworkRegistered
        } ?: return null
        val cell = cell(reg.cellIdentity, reg.accessNetworkTechnology) ?: return null
        return accessNetworkInfo(cell)
    }

    private fun cell(id: CellIdentity?, technology: Int): Cell? = when {
        id is CellIdentityLte -> Cell.Lte(
            id.mccString,
            id.mncString,
            id.tac.known(),
            id.ci.known(),
            id.bands.any(::lteTdd),
        )

        id is CellIdentityNr -> Cell.Nr(
            id.mccString,
            id.mncString,
            id.tac.known(),
            id.nci.takeIf { it != UNAVAILABLE_LONG },
            id.bands.any(::nrTdd),
        )

        // The cell's identity withheld: its radio of the registration.
        technology == TelephonyManager.NETWORK_TYPE_LTE -> Cell.Lte(null, null, null, null, tdd = false)

        technology == TelephonyManager.NETWORK_TYPE_NR -> Cell.Nr(null, null, null, null, tdd = false)

        else -> null
    }

    private fun Int.known(): Int? = takeIf { it != Int.MAX_VALUE }

    private companion object {
        /** Before the first service state: E-UTRAN, the lab's and the fleet's. */
        const val DEFAULT = "3GPP-E-UTRAN-FDD"

        const val UNAVAILABLE_LONG = Long.MAX_VALUE
    }
}
