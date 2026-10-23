package com.ellanetworks.mcptt.call

import android.content.Context
import android.telecom.DisconnectCause
import androidx.core.telecom.CallAttributesCompat
import androidx.core.telecom.CallControlScope
import androidx.core.telecom.CallsManager

/** The calls of the app in Telecom: Core-Telecom's CallsManager ([SystemTelecom]), a fake's in the tests. */
interface Telecom {
    /** CallsManager.addCall: returns once the call has ended in Telecom and [block]'s children have. */
    suspend fun addCall(
        attributes: CallAttributesCompat,
        onAnswer: suspend (callType: Int) -> Unit,
        onDisconnect: suspend (cause: DisconnectCause) -> Unit,
        onSetActive: suspend () -> Unit,
        onSetInactive: suspend () -> Unit,
        block: CallControlScope.() -> Unit,
    )
}

/** Telecom of the device: the app registered as a self-managed VoIP app (CAPABILITY_BASELINE). */
class SystemTelecom(context: Context) : Telecom {
    private val manager = CallsManager(context).apply { registerAppWithTelecom(CallsManager.CAPABILITY_BASELINE) }

    override suspend fun addCall(
        attributes: CallAttributesCompat,
        onAnswer: suspend (callType: Int) -> Unit,
        onDisconnect: suspend (cause: DisconnectCause) -> Unit,
        onSetActive: suspend () -> Unit,
        onSetInactive: suspend () -> Unit,
        block: CallControlScope.() -> Unit,
    ) = manager.addCall(attributes, onAnswer, onDisconnect, onSetActive, onSetInactive, block)
}
