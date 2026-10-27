package com.ellanetworks.mcptt.ui

import android.os.SystemClock
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.junit4.createComposeRule
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.session.Event
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

private const val GROUP = "sip:fire@mcptt.example"

/** What the home screen's PTT shows of the registration, the selected group's call and a press that failed. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class PttOfTest {
    @get:Rule
    val compose = createComposeRule()

    private data class Input(val registered: Boolean, val call: CallInfo?, val failed: Failed?)

    private val input = mutableStateOf<Input?>(null)
    private var shown: Pair<PttState, String>? = null

    /** One composition per test (setContent once), recomposed of each input. */
    private fun shown(registered: Boolean = true, call: CallInfo? = null, failed: Failed? = null): Pair<PttState, String> {
        if (input.value == null) compose.setContent { input.value?.let { shown = pttOf(it.registered, it.call, it.failed) } }
        input.value = Input(registered, call, failed)
        compose.waitForIdle()
        return requireNotNull(shown)
    }

    private fun call(state: CallInfo.State = CallInfo.State.ACTIVE) = CallInfo("c1", GROUP, null, state)

    private val now get() = SystemClock.elapsedRealtime()

    @Test
    fun unregisteredIsUnavailable() {
        assertEquals(PttState.UNAVAILABLE to "Not registered", shown(registered = false, call = call()))
    }

    @Test
    fun noCallIsIdle() {
        assertEquals(PttState.IDLE to "Push to talk", shown())
    }

    // TS 24.379 §10.1.1.4.1.1: a press that set up no call tells why, for a while.
    @Test
    fun failedPressIsBusyWithItsReason() {
        assertEquals(PttState.BUSY to "Nobody else in fire", shown(failed = Failed(GROUP, 480, now)))
        assertEquals(PttState.BUSY to "Not allowed to call", shown(failed = Failed(GROUP, 403, now)))
        assertEquals(PttState.BUSY to "Call failed", shown(failed = Failed(GROUP, 500, now)))
    }

    @Test
    fun oldFailureIsForgotten() {
        assertEquals(PttState.IDLE to "Push to talk", shown(failed = Failed(GROUP, 480, now - TRANSIENT_MS - 1)))
    }

    @Test
    fun callStates() {
        assertEquals(PttState.UNAVAILABLE to "On hold", shown(call = call(CallInfo.State.HELD)))
        assertEquals(PttState.WAITING to "Connecting…", shown(call = call(CallInfo.State.CALLING)))
        assertEquals(PttState.TALKING to "Talking", shown(call = call().copy(transmitting = true)))
        assertEquals(PttState.IDLE to "Push to talk", shown(call = call()))
    }

    @Test
    fun someoneElseTalking() {
        val c = call().copy(floor = Event.Notice.TAKEN, talker = "sip:alice@mcptt.example")
        assertEquals(PttState.RECEIVING to "alice is talking", shown(call = c))
    }

    // The user's own grant, before its first frame, is no one else talking.
    @Test
    fun ownGrantIsNotReceiving() {
        val c = call().copy(floor = Event.Notice.GRANTED, talker = "sip:me@mcptt.example")
        assertEquals(PttState.IDLE to "Push to talk", shown(call = c))
    }

    // TS 22.179 R-6.2.3.2-005, R-6.2.3.5-004: a refusal and a revocation are told, for a while.
    @Test
    fun refusedAndRevoked() {
        val busy = call().copy(floor = Event.Notice.TAKEN, talker = "sip:alice@mcptt.example", refused = now)
        assertEquals(PttState.BUSY to "Busy: alice is talking", shown(call = busy))
        assertEquals(PttState.BUSY to "Busy", shown(call = call().copy(floor = Event.Notice.DENIED, refused = now)))
        assertEquals(PttState.BUSY to "Your talk time ended", shown(call = call().copy(floor = Event.Notice.REVOKED, refused = now)))
        val old = busy.copy(refused = now - TRANSIENT_MS - 1)
        assertEquals(PttState.RECEIVING to "alice is talking", shown(call = old))
    }
}
