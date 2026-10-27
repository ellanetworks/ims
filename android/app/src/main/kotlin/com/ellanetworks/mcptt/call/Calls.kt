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

/** The name of a group of its URI: its user part. */
fun groupName(uri: String): String = uri.substringAfter(':').substringBefore('@')

/** What the calls need of the session: its calls by ID, and their speech (the Go package mobile's Session). */
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

/**
 * A call that ended, and how: for the missed and lost calls' notifications; [status] the SIP status of a REJECTED
 * INVITE (Event.Ended), [error] the session's error of a failure.
 *
 * A press whose call failed to set up is [setupFailed]: the user's own call (no caller) ended still CALLING, its INVITE
 * REJECTED (480 of the controlling function when no other member is affiliated, 403 when not allowed) or an ERROR (no
 * answer, the network, the session's refusal). Calls has played the denied cue for it; the UI tells why (TS 22.179
 * R-6.2.3.2-002, -005), from [status].
 */
data class Ended(val call: CallInfo, val cause: Event.Cause, val status: Int = 0, val error: String? = null) {
    val setupFailed: Boolean
        get() = call.caller == null &&
            call.state == CallInfo.State.CALLING &&
            (cause == Event.Cause.REJECTED || cause == Event.Cause.ERROR)
}

/** A call, as the UI and the notification show it. */
data class CallInfo(
    val id: String,
    val group: String,
    /** The inviting user's MCPTT ID of an invitation; null of the user's own call. */
    val caller: String?,
    val state: State,
    /** The last notice of the floor participant (TS 24.380 §6.2.4). */
    val floor: Event.Notice? = null,
    /** The MCPTT ID of who has the floor, the user's own when granted. */
    val talker: String? = null,
    val transmitting: Boolean = false,
    /** Telecom muted the call, or holds it: the PTT refused. */
    val muted: Boolean = false,
    /** The audio route (CallEndpointCompat's type), and the routes Telecom offers. */
    val endpoint: Int? = null,
    val routes: List<Int> = emptyList(),
    /** While the user has the floor, when its talk time ends (SystemClock.elapsedRealtime), and how long it is. */
    val talkUntil: Long? = null,
    val talkFor: Long? = null,
    /** When the floor was last refused or revoked (SystemClock.elapsedRealtime): each refusal shown, if alike. */
    val refused: Long? = null,
    /** When the call's audio last failed, its playout or capture rebuilt in vain (SystemClock.elapsedRealtime). */
    val audioFailed: Long? = null,
) {
    enum class State {
        /** Placed, not yet answered. */
        CALLING,

        /** An invitation of manual commencement (TS 24.379 §6.2.3.2), for the user to accept or decline. */
        RINGING,
        ACTIVE,

        /** Telecom holds it for another app's call: no speech in or out. */
        HELD,
    }
}

/**
 * The calls of the session through Telecom (Core-Telecom's self-managed VoIP calls): each a call of the system, so that
 * Telecom owns the call audio (MODE_IN_COMMUNICATION, its focus and routes), holds it for a cellular call, and lists it
 * on watches and cars. An invitation of automatic commencement (TS 24.379 §6.2.3.1), answered by the session at once,
 * is answered in Telecom without the user; one of manual commencement rings. Telecom's own requests (answer,
 * disconnect, hold) go to the session. Each active call plays its speech, and captures while the client transmits.
 * The session's calls of the user's ([CallOps]: they bind sockets and send SIP) run off the caller's thread, the main
 * one of the UI's, on one queue: in the order asked, a release never before its press.
 *
 * [recordable] tells whether the process may record now, beyond the permission: its service holds the microphone type,
 * or the app is visible (the while-in-use rule). Without, Android silences the recorder, so a press would send
 * silence: it is refused instead, and told on [unrecordable].
 */
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

    /** In the order tracked: the last the newest. */
    private val calls = LinkedHashMap<String, Call>()
    private val state = MutableStateFlow<List<CallInfo>>(emptyList())

    /** Telecom adds one call at a time (CallsManager.addCall): held until its scope runs. */
    private val setup = Mutex()

    /** The calls, newest last. */
    val current: StateFlow<List<CallInfo>> = state.asStateFlow()

    private val endings = MutableSharedFlow<Ended>(extraBufferCapacity = ENDINGS)

    /** The calls as they end. */
    val ended: SharedFlow<Ended> = endings.asSharedFlow()

    private val refusals = MutableSharedFlow<Unit>(extraBufferCapacity = 1)

    /** A press refused for the microphone the process may not use now, its permission granted: the app to open. */
    val unrecordable: SharedFlow<Unit> = refusals.asSharedFlow()

    /**
     * The courtesy tones: the floor free, the call lost, which the UI shows as well (TS 22.179 R-6.2.3.4-003). The
     * talker's own are always played: the grant and the refusals (R-6.2.3.2-004, -005), the talk time's warning and end
     * (R-6.2.3.5-003, -004), indications the talker must get with the phone at the mouth, the screen out of sight.
     */
    @Volatile
    var tones = true

    /**
     * The encoder's mode the session last asked for: a call's capture started in it, before its own Transmit tells
     * it, so that its encoder is not built again at the talk permit (Capture).
     */
    @Volatile
    private var lastMode = 0

    private inner class Call(id: String, group: String, caller: String?, initial: CallInfo.State) {
        val info = MutableStateFlow(CallInfo(id, group, caller, initial))
        val ended = CompletableDeferred<DisconnectCause>()
        val control = CompletableDeferred<CallControlScope>()
        val playout = audio.playout({ ops.nextSpeech(id) }) { audioFailed(this, capturing = false, it) }
        val capture = audio.capture({ ops.sendSpeech(id, it) }, { audioFailed(this, capturing = true, it) }) { captureReady(this) }
        val id get() = info.value.id

        /** Telecom has the call active: its audio Telecom's (the mode, focus and route of a call). */
        @Volatile
        var telecomActive = false

        /** The encoder's mode of the transmission. */
        @Volatile
        var mode = lastMode

        /** The endpoints Telecom offers now; routed, that the user chose one (route): the speaker no more imposed. */
        @Volatile
        var available: List<CallEndpointCompat> = emptyList()

        @Volatile
        var routed = false

        /** When Telecom held the call: the last held is the one resumed. */
        @Volatile
        var heldAt = 0L

        /** Pressed while another talks: told busy at once, the server's Floor Deny then silent. */
        @Volatile
        var busyPressed = false

        /** The talk-time warning, while the user has the floor. */
        var warning: Job? = null

        /** The floor granted before the microphone records (SystemClock.elapsedRealtime): its talk permit told once it does. */
        @Volatile
        var permitPending: Long? = null

        /** Plays the speech while active in the session and in Telecom, and records it, sent while transmitting. */
        @Synchronized
        fun media() {
            // Ended: its audio stopped for good (end), not started again by a later event's media.
            if (ended.isCompleted) {
                playout.stop()
                capture.stop()
                return
            }
            val i = info.value
            val active = i.state == CallInfo.State.ACTIVE && telecomActive
            if (active) playout.start() else playout.stop()
            // The microphone ready while the call is up in the session, from the press that places one (the recorder
            // 400 to 800 ms to start, the floor granted with the call's 200), not waiting for Telecom, whose activation
            // would restart it; its frames sent while the session's call transmits: the talk permit is then
            // the moment speech is captured, none of its start lost (TS 22.179 R-6.15.3.2-016).
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

    /** Runs [f], a call of the session, on the queue. */
    private fun later(f: () -> Unit) {
        queue.trySend(f)
    }

    /** Runs [f] on the queue, returning once it has. */
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

    /** An event of a call of the session, in the session's order. */
    fun event(e: Event.OfCall) {
        events.trySend(e)
    }

    /**
     * The PTT pressed for [group], as a radio's: with no call of the group, the call placed, its INVITE the floor request
     * (TS 24.380 §14.2.5); with one, a floor request in it; an invitation ringing, answered. The call pressed, to
     * release; null if the PTT cannot talk now (no microphone, the call held or muted): the denied cue then. Decided
     * at once, of the calls' state; the session's part queued. A call placed that fails to set up is told on [ended]
     * (Ended.setupFailed), the denied cue played.
     */
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

    /** Places a prearranged group call to [group] (TS 24.379 §10.1.1.2.1.1): its ID, its INVITE queued. */
    private fun place(group: String): String {
        val id = UUID.randomUUID().toString()
        // Known before the session's first event of it.
        val c = track(Call(id, group, null, CallInfo.State.CALLING))
        // The microphone started with the press: its recorder's start, not the call's setup, the talk permit's wait.
        c.media()
        later {
            // Ended before its turn (the session gone): not placed.
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

    /** The user accepts an invitation of manual commencement. */
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

    /** The microphone records now: allowed, and [recordable]. */
    private fun microphone(): Boolean = allowed() && recordable()

    /** A press may talk: the [microphone]'s; allowed but not recordable told on [unrecordable], the app to open. */
    private fun talkable(): Boolean {
        if (microphone()) return true
        if (allowed()) refusals.tryEmit(Unit)
        return false
    }

    /** Presses the PTT of [c]: refused while Telecom mutes or holds the call, or without the microphone. */
    private fun press(c: Call): Boolean {
        val id = c.id
        val i = c.info.value
        if (i.state != CallInfo.State.ACTIVE || !c.telecomActive || i.muted || !talkable()) return false
        // Another talks: busy at once, as a radio's, not of the server's answer, which a tap released first never
        // gets (no queueing: the Floor Release before the Floor Deny). The request still goes: the server decides
        // (TS 22.179 R-6.2.3.2-005), its grant the talk.
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

    /** Routes the call's audio to the endpoint of [type] Telecom offers (CallControlScope.availableEndpoints). */
    fun route(id: String, type: Int) = scope.launch {
        val c = synchronized(calls) { calls[id] } ?: return@launch
        val endpoint = c.available.firstOrNull { it.type == type } ?: return@launch
        c.routed = true
        c.control.await().requestEndpointChange(endpoint)
    }

    /** Ends every call: the session gone, its calls with it. */
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
                // An outgoing call answered, active in Telecom too (an incoming one is, of answer).
                if (c.info.value.caller == null) scope.launch { activate(c) { setActive() } }
            }

            is Event.Ended -> {
                val ended = Ended(c.info.value, e.cause, e.status, e.error)
                when {
                    // The press of a call that failed to set up told, not silent (TS 22.179 R-6.2.3.2-002, -005).
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

    /** The cue of a floor notice (Cues): the talker's always, the floor free as [tones]. */
    private fun floorTone(c: Call, before: CallInfo, notice: Event.Notice) {
        when (notice) {
            // A grant told again with its talk time (mc_granted) chirps once.
            Event.Notice.GRANTED -> if (before.floor != Event.Notice.GRANTED) permit(c)

            Event.Notice.DENIED -> if (!c.busyPressed) cues.denied()

            Event.Notice.IDLE -> if (tones && before.floor == Event.Notice.TAKEN) cues.free()

            // Revoked, or the talk time reached (TS 22.179 R-6.2.3.5-004), or the request unanswered (R-6.2.3.2-005).
            Event.Notice.REVOKED, Event.Notice.TIMEOUT -> cues.revoked()

            else -> Unit
        }
    }

    /**
     * The user may talk [seconds] (the grant's Duration, TS 24.380 §8.2.3.3): the time shown, and a warning [WARN_MS]
     * before it ends (TS 22.179 R-6.2.3.5-003); its end is the server's Floor Revoke (R-6.2.3.5-004, -005).
     */
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

    /** Makes [c] active in Telecom by [request] (answer or setActive): refused, the session's call is left. */
    private suspend fun activate(c: Call, request: suspend CallControlScope.() -> CallControlResult) {
        val control = try {
            c.control.await()
        } catch (_: CancellationException) {
            return
        }
        when (val r = control.request()) {
            is CallControlResult.Error -> {
                // Another app's call Telecom cannot hold: that call first.
                Log.w(TAG, "call ${c.id} not active in Telecom: $r")
                later { ops.leave(c.id) }
            }

            else -> activated(c)
        }
    }

    /** [c] active in Telecom: its audio Telecom's, the speech played and captured. */
    private suspend fun activated(c: Call) {
        // A held call resumed: active again in the session's state as well.
        if (c.info.value.state == CallInfo.State.HELD) {
            hold(c, false)
        } else {
            c.telecomActive = true
            c.media()
        }
        speaker(c)
    }

    /** Adds [c] to Telecom, which keeps the call until it ends: refused by Telecom, the session's call is left. */
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
        // Ended while another call was set up: not added.
        if (c.ended.isCompleted) {
            setup.unlock()
            return
        }
        try {
            telecom.addCall(
                attributes,
                // Answered by Telecom (a headset's, car's or watch's button): accepted in the session, and active, as
                // Telecom then has it (CallsManager.addCall: "answer an incoming call and set it to active"; Google's
                // Telecom sample).
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
                // The endpoints' flows have one collector each: each value is taken once.
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
                    // The flows' collectors too: addCall returns once its children have.
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

    /**
     * The active call on the speaker, as a radio's: heard without holding the phone to the ear (the specs leave it to the
     * UE), unless Telecom has it on a headset or Bluetooth, or the user chose the earpiece. Telecom puts a call on the
     * earpiece, when it becomes active as well: the speaker asked for once the call is active, and again each time
     * Telecom moves it back to the earpiece, so that the request is not undone by the activation.
     */
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
        // Telecom requires the speech stopped when onSetInactive returns.
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
        // Never added to Telecom: nothing waits for its scope.
        c.control.cancel()
        c.telecomActive = false
        c.playout.stop()
        c.capture.stop()
        if (removed) publish()
        // The active call ended: the call it held, the last, active again. One another app's call holds is left to
        // the user, as Telecom resumes none.
        if (removed && active && held != null) scope.launch { activate(held) { setActive() } }
    }

    /**
     * The talk permit (TS 22.179 R-6.2.3.2-004): at once if the microphone records; else once it does (captureReady),
     * so that the talker speaks to a microphone that hears, none of the speech's start lost (R-6.15.3.2-016). Not
     * recording within [PERMIT_MS], the floor is given back and the press refused, rather than a permit told to talk
     * unheard.
     */
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
        // The wait for the microphone, of the access time the user perceives (TS 22.179 §6.15.3.2, KPI 2).
        Log.i(TAG, "call ${c.id}: talk permit after the microphone, ${SystemClock.elapsedRealtime() - since} ms")
        if (c.info.value.floor == Event.Notice.GRANTED) cues.grant()
    }

    /**
     * The audio of [c] failed, rebuilt in vain (Worker): shown; while the user talks, nothing would be sent, so the
     * floor released and the user told. The next change of the call's state starts it again.
     */
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

    /** The CPU awake while a call lasts: its speech every 20 ms, its floor control. Called holding [calls]. */
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

        /** Telecom takes ERROR from SDK 38 on, refusing it before: LOCAL then, with the reason (CallControlScope.disconnect). */
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

            // An INVITE refused, of a call placed: an error of the call, not the user's rejection.
            Event.Cause.REJECTED, Event.Cause.ERROR -> error()
        }
    }
}
