package com.ellanetworks.mcptt.ui

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.ellanetworks.mcptt.LogLine
import com.ellanetworks.mcptt.McpttApp
import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.call.Ended
import com.ellanetworks.mcptt.call.PttController
import com.ellanetworks.mcptt.service.SessionService
import com.ellanetworks.mcptt.settings.UserSettings
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch

class AppViewModel(private val app: McpttApp) : ViewModel() {
    fun hasCarrierPrivileges(): Boolean = app.sessions.hasCarrierPrivileges()

    val status: StateFlow<Status> = app.sessions.status
    val log: StateFlow<List<LogLine>> = app.sessions.log
    val calls: StateFlow<List<CallInfo>> = app.sessions.calls.current
    val ended: SharedFlow<Ended> = app.sessions.calls.ended
    val microphone: StateFlow<Boolean> = app.sessions.microphone
    val debug: StateFlow<Boolean> = app.sessions.debug
    val settings: StateFlow<UserSettings?> =
        app.settings.data.stateIn(viewModelScope, SharingStarted.WhileSubscribed(STOP_TIMEOUT_MS), null)

    fun save(s: UserSettings) = update { s.copy(clientId = it.clientId) }

    fun update(change: (UserSettings) -> UserSettings) {
        viewModelScope.launch { app.settings.updateData(change) }
    }

    fun select(group: String) = update { it.copy(selectedGroup = group) }

    fun start() = SessionService.start(app, visible = true)

    fun stop() = SessionService.stop(app)

    fun visible() = SessionService.visible(app, true)

    fun talk(group: String): String? = app.sessions.ptt.press(PttController.Source.SCREEN, group)

    fun release() = app.sessions.ptt.release(PttController.Source.SCREEN)

    fun accept(id: String) {
        app.sessions.calls.accept(id)
    }

    fun decline(id: String) = app.sessions.calls.decline(id)

    fun leave(id: String) = app.sessions.calls.leave(id)

    fun route(id: String, type: Int) = app.sessions.calls.route(id, type)

    fun clearLog() = app.sessions.clearLog()

    fun setDebug(on: Boolean) = app.sessions.setDebug(on)

    private companion object {
        const val STOP_TIMEOUT_MS = 5_000L
    }
}
