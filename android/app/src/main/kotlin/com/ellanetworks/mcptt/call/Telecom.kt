package com.ellanetworks.mcptt.call

import android.content.Context
import android.telecom.DisconnectCause
import androidx.core.telecom.CallAttributesCompat
import androidx.core.telecom.CallControlScope
import androidx.core.telecom.CallsManager

interface Telecom {
    suspend fun addCall(
        attributes: CallAttributesCompat,
        onAnswer: suspend (callType: Int) -> Unit,
        onDisconnect: suspend (cause: DisconnectCause) -> Unit,
        onSetActive: suspend () -> Unit,
        onSetInactive: suspend () -> Unit,
        block: CallControlScope.() -> Unit,
    )
}

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
