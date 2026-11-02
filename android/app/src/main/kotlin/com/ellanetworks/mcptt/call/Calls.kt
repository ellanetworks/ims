package com.ellanetworks.mcptt.call

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import android.os.PowerManager
import android.os.SystemClock
import android.telecom.DisconnectCause
import android.util.Log
import androidx.core.content.ContextCompat
import androidx.core.net.toUri
import androidx.core.telecom.CallAttributesCompat
import androidx.core.telecom.CallControlResult
import androidx.core.telecom.CallControlScope
import androidx.core.telecom.CallEndpointCompat
import com.ellanetworks.mcptt.media.Audio
import com.ellanetworks.mcptt.media.Cues
import com.ellanetworks.mcptt.media.DeviceAudio
import com.ellanetworks.mcptt.media.DeviceCues
import com.ellanetworks.mcptt.session.Event
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import java.util.UUID

fun groupName(uri: String): String = uri.substringAfter(':').substringBefore('@')

interface CallOps {
    fun place(id: String, group: String)
    fun accept(id: String)
    fun decline(id: String)
    fun leave(id: String)
    fun press(id: String)
    fun release(id: String)
    fun nextSpeech(id: String): ByteArray
    fun sendSpeech(id: String, frame: ByteArray)
}

data class Ended(val call: CallInfo, val cause: Event.Cause, val status: Int = 0, val error: String? = null) {
    val setupFailed: Boolean
        get() = call.caller == null &&
            call.state == CallInfo.State.CALLING &&
            (cause == Event.Cause.REJECTED || cause == Event.Cause.ERROR)
}

data class CallInfo(
    val id: String,
    val group: String,
    val caller: String?,
    val state: State,
    val floor: Event.Notice? = null,
    val talker: String? = null,
    val transmitting: Boolean = false,
    val muted: Boolean = false,
    val endpoint: Int? = null,
    val routes: List<Int> = emptyList(),
    val talkUntil: Long? = null,
    val talkFor: Long? = null,
    val refused: Long? = null,
    val audioFailed: Long? = null,
) {
    enum class State {
        CALLING,

        RINGING,
        ACTIVE,

        HELD,
    }
}

class Calls(
    private val context: Context,
    private val ops: CallOps,
    private val self: () -> String?,
    private val recordable: () -> Boolean,
    private val telecom: Telecom = SystemTelecom(context),
    private val cues: Cues = DeviceCues(context),
    private val audio: Audio = DeviceAudio,
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val lock = context.getSystemService(PowerManager::class.java)
        .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "ellamcptt:call")
        .apply { setReferenceCounted(false) }

    private val events = Channel<Event.OfCall>(Channel.UNLIMITED)
    private val queue = Channel<() -> Unit>(Channel.UNLIMITED)

    private val calls = LinkedHashMap<String, Call>()
    private val state = MutableStateFlow<List<CallInfo>>(emptyList())

    private val setup = Mutex()

    val current: StateFlow<List<CallInfo>> = state.asStateFlow()

    private val endings = MutableSharedFlow<Ended>(extraBufferCapacity = ENDINGS)

    val ended: SharedFlow<Ended> = endings.asSharedFlow()

    private val refusals = MutableSharedFlow<Unit>(extraBufferCapacity = 1)

    val unrecordable: SharedFlow<Unit> = refusals.asSharedFlow()

    @Volatile
    var tones = true

    @Volatile
    private var lastMode = 0

    private inner class Call(id: String, group: String, caller: String?, initial: CallInfo.State) {
        val info = MutableStateFlow(CallInfo(id, group, caller, initial))
        val ended = CompletableDeferred<DisconnectCause>()
        val control = CompletableDeferred<CallControlScope>()
        val playout = audio.playout({ ops.nextSpeech(id) }) { audioFailed(this, capturing = false, it) }
        val capture = audio.capture({ ops.sendSpeech(id, it) }, { audioFailed(this, capturing = true, it) }) { captureReady(this) }
        val id get() = info.value.id

        @Volatile
        var telecomActive = false

        @Volatile
        var mode = lastMode

        @Volatile
        var available: List<CallEndpointCompat> = emptyList()

        @Volatile
        var routed = false

        @Volatile
        var heldAt = 0L

        @Volatile
        var busyPressed = false

        var warning: Job? = null

        @Volatile
        var permitPending: Long? = null

        @Synchronized
        fun media() {
            if (ended.isCompleted) {
                playout.stop()
                capture.stop()
                return
            }
            val i = info.value
            val active = i.state == CallInfo.State.ACTIVE && telecomActive
            if (active) playout.start() else playout.stop()
            val up = i.state == CallInfo.State.ACTIVE || (i.state == CallInfo.State.CALLING && i.caller == null)
            if (up && !i.muted && microphone()) capture.start(mode) else capture.stop()
            capture.send(i.state == CallInfo.State.ACTIVE && i.transmitting)
        }
    }

    init {
        scope.launch(Dispatchers.IO) {
            for (f in queue) {
                try {
                    f()
                } catch (x: Exception) {
                    Log.e(TAG, "call op: $x")
                }
            }
        }
        scope.launch {
            for (e in events) {
                try {
                    handle(e)
                } catch (x: Exception) {
                    Log.e(TAG, "call event $e: $x")
                }
            }
        }
    }

    private fun later(f: () -> Unit) {
        queue.trySend(f)
    }

    private suspend fun serially(f: () -> Unit) {
        val done = CompletableDeferred<Unit>()
        later {
            try {
                f()
            } finally {
                done.complete(Unit)
            }
        }
        done.await()
    }

    fun event(e: Event.OfCall) {
        events.trySend(e)
    }

    // TS 24.380 clause 14.2.5
    fun talk(group: String): String? {
        val c = synchronized(calls) { calls.values.lastOrNull { it.info.value.group == group } }
        val id = when {
            !talkable() -> null
            c == null -> place(group)
            c.info.value.state == CallInfo.State.RINGING -> c.id.also { accept(it) }
            c.info.value.state == CallInfo.State.CALLING -> c.id
            press(c) -> c.id
            else -> null
        }
        if (id == null) cues.denied()
        return id
    }

    // TS 24.379 clause 10.1.1.2.1.1
    private fun place(group: String): String {
        val id = UUID.randomUUID().toString()
        val c = track(Call(id, group, null, CallInfo.State.CALLING))
        c.media()
        later {
            if (c.ended.isCompleted) return@later
            try {
                ops.place(id, group)
            } catch (e: Exception) {
                Log.w(TAG, "call to $group: $e")
                cues.denied()
                endings.tryEmit(Ended(c.info.value, Event.Cause.ERROR, error = e.message ?: e.toString()))
                end(c, error())
                return@later
            }
            scope.launch { add(c, CallAttributesCompat.DIRECTION_OUTGOING) }
        }
        return id
    }

    fun accept(id: String) {
        val c = synchronized(calls) { calls[id] } ?: return
        if (c.info.value.state != CallInfo.State.RINGING) return
        val accepted = CompletableDeferred<Unit>()
        later {
            try {
                ops.accept(id)
            } finally {
                accepted.complete(Unit)
            }
        }
        scope.launch {
            accepted.await()
            activate(c) { answer(CallAttributesCompat.CALL_TYPE_AUDIO_CALL) }
        }
    }

    fun decline(id: String) = later { ops.decline(id) }

    fun leave(id: String) = later { ops.leave(id) }

    private fun allowed(): Boolean = ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    private fun microphone(): Boolean = allowed() && recordable()

    private fun talkable(): Boolean {
        if (microphone()) return true
        if (allowed()) refusals.tryEmit(Unit)
        return false
    }

    private fun press(c: Call): Boolean {
        val id = c.id
        val i = c.info.value
        if (i.state != CallInfo.State.ACTIVE || !c.telecomActive || i.muted || !talkable()) return false
        c.busyPressed = i.floor == Event.Notice.TAKEN
        if (c.busyPressed) {
            cues.denied()
            c.info.update { it.copy(refused = SystemClock.elapsedRealtime()) }
            publish()
        }
        later { ops.press(id) }
        return true
    }

    fun release(id: String) {
        synchronized(calls) { calls[id] }?.busyPressed = false
        later { ops.release(id) }
    }

    fun route(id: String, type: Int) = scope.launch {
        val c = synchronized(calls) { calls[id] } ?: return@launch
        val endpoint = c.available.firstOrNull { it.type == type } ?: return@launch
        c.routed = true
        c.control.await().requestEndpointChange(endpoint)
    }

    fun clear() {
        val all = synchronized(calls) { calls.values.toList() }
        for (c in all) end(c, error())
    }

    private fun handle(e: Event.OfCall) {
        if (e is Event.Incoming) {
            val c = track(Call(e.call, e.group, e.caller, if (e.manual) CallInfo.State.RINGING else CallInfo.State.CALLING))
            scope.launch { add(c, CallAttributesCompat.DIRECTION_INCOMING, autoAnswer = !e.manual) }
            return
        }
        val c = synchronized(calls) { calls[e.call] } ?: return
        when (e) {
            is Event.Established -> {
                c.info.update { if (it.state == CallInfo.State.HELD) it else it.copy(state = CallInfo.State.ACTIVE) }
                if (c.info.value.caller == null) scope.launch { activate(c) { setActive() } }
            }

            is Event.Ended -> {
                val ended = Ended(c.info.value, e.cause, e.status, e.error)
                when {
                    ended.setupFailed -> cues.denied()
                    e.cause == Event.Cause.ERROR && ended.call.state == CallInfo.State.ACTIVE && tones -> cues.lost()
                }
                endings.tryEmit(ended)
                end(c, disconnectCause(e.cause))
            }

            is Event.Floor -> {
                val before = c.info.value
                floorTone(c, before, e.notice)
                if (e.notice == Event.Notice.GRANTED && e.duration != null) talkTime(c, e.duration)
                c.info.update {
                    it.copy(
                        floor = e.notice,
                        refused = if (e.notice == Event.Notice.DENIED || e.notice == Event.Notice.REVOKED) SystemClock.elapsedRealtime() else it.refused,
                        talker = when (e.notice) {
                            Event.Notice.TAKEN -> e.party
                            Event.Notice.GRANTED -> self()
                            Event.Notice.IDLE, Event.Notice.REVOKED -> null
                            else -> it.talker
                        },
                    )
                }
            }

            is Event.Transmit -> {
                e.mode?.let {
                    c.mode = it
                    lastMode = it
                }
                c.info.update { if (e.on) it.copy(transmitting = true) else it.copy(transmitting = false, talkUntil = null, talkFor = null) }
                if (!e.on) c.warning?.cancel()
            }

            is Event.ModeRequest -> {
                c.mode = e.mode
                lastMode = e.mode
                c.capture.mode(e.mode)
            }

            else -> Unit
        }
        c.media()
        publish()
    }

    private fun floorTone(c: Call, before: CallInfo, notice: Event.Notice) {
        when (notice) {
            Event.Notice.GRANTED -> if (before.floor != Event.Notice.GRANTED) permit(c)
            Event.Notice.DENIED -> if (!c.busyPressed) cues.denied()
            Event.Notice.IDLE -> if (tones && before.floor == Event.Notice.TAKEN) cues.free()
            Event.Notice.REVOKED, Event.Notice.TIMEOUT -> cues.revoked()
            else -> Unit
        }
    }

    // TS 24.380 clause 8.2.3.3
    private fun talkTime(c: Call, seconds: Int) {
        if (seconds <= 0) return
        val total = seconds * 1000L
        c.info.update { it.copy(talkUntil = SystemClock.elapsedRealtime() + total, talkFor = total) }
        c.warning?.cancel()
        if (total > 2 * WARN_MS) {
            c.warning = scope.launch {
                delay(total - WARN_MS)
                if (c.info.value.transmitting) cues.warning()
            }
        }
    }

    private fun track(c: Call): Call {
        synchronized(calls) {
            calls[c.id] = c
            awake()
        }
        publish()
        return c
    }

    private suspend fun activate(c: Call, request: suspend CallControlScope.() -> CallControlResult) {
        val control = try {
            c.control.await()
        } catch (_: CancellationException) {
            return
        }
        when (val r = control.request()) {
            is CallControlResult.Error -> {
                Log.w(TAG, "call ${c.id} not active in Telecom: $r")
                later { ops.leave(c.id) }
            }

            else -> activated(c)
        }
    }

    private suspend fun activated(c: Call) {
        if (c.info.value.state == CallInfo.State.HELD) {
            hold(c, false)
        } else {
            c.telecomActive = true
            c.media()
        }
        speaker(c)
    }

    private suspend fun add(c: Call, direction: Int, autoAnswer: Boolean = false) {
        val i = c.info.value
        val attributes = CallAttributesCompat(
            displayName = groupName(i.group),
            address = i.group.toUri(),
            direction = direction,
            callType = CallAttributesCompat.CALL_TYPE_AUDIO_CALL,
            callCapabilities = CallAttributesCompat.SUPPORTS_SET_INACTIVE,
        )
        setup.lock()
        if (c.ended.isCompleted) {
            setup.unlock()
            return
        }
        try {
            telecom.addCall(
                attributes,
                onAnswer = {
                    if (c.info.value.state == CallInfo.State.RINGING) later { ops.accept(c.id) }
                    activated(c)
                },
                onDisconnect = {
                    val ringing = c.info.value.state == CallInfo.State.RINGING
                    later { if (ringing) ops.decline(c.id) else ops.leave(c.id) }
                },
                onSetActive = { hold(c, false) },
                onSetInactive = { hold(c, true) },
            ) {
                setup.unlock()
                c.control.complete(this)
                launch { isMuted.collect { muted(c, it) } }
                launch {
                    availableEndpoints.collect { list ->
                        c.available = list
                        c.info.update { it.copy(routes = list.map(CallEndpointCompat::type)) }
                        publish()
                    }
                }
                launch {
                    currentCallEndpoint.collect { ep ->
                        c.info.update { it.copy(endpoint = ep.type) }
                        publish()
                        if (c.telecomActive) speaker(c)
                    }
                }
                if (autoAnswer) launch { activate(c) { answer(CallAttributesCompat.CALL_TYPE_AUDIO_CALL) } }
                launch {
                    disconnect(c.ended.await())
                    this@addCall.coroutineContext.cancelChildren()
                }
            }
        } catch (e: Exception) {
            if (setup.isLocked && !c.control.isCompleted) setup.unlock()
            Log.w(TAG, "Telecom refused call ${c.id}: $e")
            if (!c.ended.isCompleted) {
                val ringing = c.info.value.state == CallInfo.State.RINGING
                later { if (ringing) ops.decline(c.id) else ops.leave(c.id) }
            }
            end(c, error())
        }
    }

    private suspend fun speaker(c: Call) {
        if (c.routed || c.info.value.endpoint != CallEndpointCompat.TYPE_EARPIECE) return
        val speaker = c.available.firstOrNull { it.type == CallEndpointCompat.TYPE_SPEAKER } ?: return
        val control = c.control.takeIf { it.isCompleted }?.await() ?: return
        control.requestEndpointChange(speaker)
    }

    private suspend fun hold(c: Call, held: Boolean) {
        c.telecomActive = !held
        if (held) c.heldAt = SystemClock.elapsedRealtime()
        c.info.update {
            when {
                held -> it.copy(state = CallInfo.State.HELD)
                it.state == CallInfo.State.HELD -> it.copy(state = CallInfo.State.ACTIVE)
                else -> it
            }
        }
        if (held) serially { ops.release(c.id) }
        c.media()
        publish()
    }

    private fun muted(c: Call, muted: Boolean) {
        c.info.update { it.copy(muted = muted) }
        if (muted) later { ops.release(c.id) }
        c.media()
        publish()
    }

    private fun end(c: Call, cause: DisconnectCause) {
        val active = c.telecomActive
        val (removed, held) = synchronized(calls) {
            val removed = (calls.remove(c.id) != null).also { if (it) awake() }
            removed to calls.values.filter { it.info.value.state == CallInfo.State.HELD }.maxByOrNull { it.heldAt }
        }
        c.ended.complete(cause)
        c.warning?.cancel()
        c.permitPending = null
        c.control.cancel()
        c.telecomActive = false
        c.playout.stop()
        c.capture.stop()
        if (removed) publish()
        if (removed && active && held != null) scope.launch { activate(held) { setActive() } }
    }

    private fun permit(c: Call) {
        if (c.capture.ready) {
            cues.grant()
            return
        }
        val since = SystemClock.elapsedRealtime()
        c.permitPending = since
        scope.launch {
            delay(PERMIT_MS)
            if (c.permitPending != since) return@launch
            c.permitPending = null
            Log.w(TAG, "call ${c.id}: no microphone ${PERMIT_MS} ms after the grant: the floor released")
            if (c.info.value.floor == Event.Notice.GRANTED) {
                cues.denied()
                later { ops.release(c.id) }
            }
        }
    }

    private fun captureReady(c: Call) {
        val since = c.permitPending ?: return
        c.permitPending = null
        Log.i(TAG, "call ${c.id}: talk permit after the microphone, ${SystemClock.elapsedRealtime() - since} ms")
        if (c.info.value.floor == Event.Notice.GRANTED) cues.grant()
    }

    private fun audioFailed(c: Call, capturing: Boolean, error: String) {
        Log.e(TAG, "call ${c.id}: ${if (capturing) "capture" else "playout"} failed: $error")
        c.info.update { it.copy(audioFailed = SystemClock.elapsedRealtime()) }
        if (capturing && c.info.value.transmitting) {
            cues.denied()
            later { ops.release(c.id) }
        }
        publish()
    }

    private fun publish() {
        state.value = synchronized(calls) { calls.values.map { it.info.value } }
    }

    private fun awake() {
        val any = calls.isNotEmpty()
        if (any && !lock.isHeld) {
            @Suppress("WakelockTimeout")
            lock.acquire()
        } else if (!any && lock.isHeld) {
            lock.release()
        }
    }

    private companion object {
        const val TAG = "EllaMcptt"

        private const val SDK_ERROR = 38
        private const val WARN_MS = 5_000L
        private const val PERMIT_MS = 3_000L
        private const val ENDINGS = 16

        fun error() = if (Build.VERSION.SDK_INT >= SDK_ERROR) {
            DisconnectCause(DisconnectCause.ERROR)
        } else {
            DisconnectCause(DisconnectCause.LOCAL, null, null, "error")
        }

        fun disconnectCause(c: Event.Cause) = when (c) {
            Event.Cause.LOCAL -> DisconnectCause(DisconnectCause.LOCAL)
            Event.Cause.REMOTE -> DisconnectCause(DisconnectCause.REMOTE)
            Event.Cause.MISSED -> DisconnectCause(DisconnectCause.MISSED)
            Event.Cause.REJECTED, Event.Cause.ERROR -> error()
        }
    }
}
