package com.ellanetworks.mcptt.call

import android.Manifest
import android.app.Application
import android.os.ParcelUuid
import android.telecom.DisconnectCause
import androidx.core.telecom.CallAttributesCompat
import androidx.core.telecom.CallControlResult
import androidx.core.telecom.CallControlScope
import androidx.core.telecom.CallEndpointCompat
import com.ellanetworks.mcptt.media.Audio
import com.ellanetworks.mcptt.media.Cues
import com.ellanetworks.mcptt.media.SpeechIn
import com.ellanetworks.mcptt.media.SpeechOut
import com.ellanetworks.mcptt.session.Event
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import java.util.Collections
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlin.coroutines.CoroutineContext

private const val GROUP = "sip:fire@mcptt.example"

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class CallsTest {
    private val ops = FakeOps()
    private val telecom = FakeTelecom()
    private val cues = FakeCues()
    private val audio = FakeAudio()
    private val ends: MutableList<Ended> = Collections.synchronizedList(mutableListOf())
    private val collector = CoroutineScope(Dispatchers.Default)
    private lateinit var calls: Calls

    @Before
    fun setUp() {
        val app = RuntimeEnvironment.getApplication()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        calls = Calls(app, ops, self = { "sip:me@mcptt.example" }, telecom, cues, audio)
        collector.launch(start = CoroutineStart.UNDISPATCHED) { calls.ended.collect { ends += it } }
    }

    @After
    fun tearDown() {
        calls.clear()
        collector.cancel()
    }

    // A1: answered by Telecom (a headset's button), the call is active: its audio on, its PTT pressed.
    @Test
    fun answeredByTelecom() {
        calls.event(Event.Incoming("in-1", GROUP, "sip:alice@mcptt.example", manual = true))
        val added = telecom.await()
        runBlocking { added.onAnswer(CallAttributesCompat.CALL_TYPE_AUDIO_CALL) }
        eventually { "accept in-1" in ops.log }
        calls.event(Event.Established("in-1"))
        eventually { calls.current.value.single().state == CallInfo.State.ACTIVE }
        eventually { audio.playing.get() == 1 }

        assertEquals("in-1", calls.talk(GROUP))
        eventually { "press in-1" in ops.log }
        assertFalse("denied" in cues.log)
    }

    // The INVITE off the caller's thread, and the release after it.
    @Test
    fun placesOffTheCallersThread() {
        val placing = CountDownLatch(1)
        ops.placing = placing
        val id = calls.talk(GROUP)
        assertNotNull(id)
        calls.release(id!!)
        assertEquals(CallInfo.State.CALLING, calls.current.value.single().state)
        placing.countDown()
        eventually { ops.log.size == 2 }
        assertEquals(listOf("place $id $GROUP", "release $id"), ops.log.toList())
    }

    // P3: a press whose INVITE fails is told: the denied cue, an end of setup failed.
    @Test
    fun placeFailed() {
        ops.fail = true
        val id = calls.talk(GROUP)
        eventually { ends.isNotEmpty() }
        val e = ends.single()
        assertEquals(id, e.call.id)
        assertEquals(Event.Cause.ERROR, e.cause)
        assertTrue(e.setupFailed)
        assertEquals(listOf("denied"), cues.log.toList())
        assertIdle()
    }

    // P3: rejected by the server (480: nobody else affiliated) while CALLING.
    @Test
    fun rejectedWhileCalling() {
        val id = requireNotNull(calls.talk(GROUP))
        eventually { "place $id $GROUP" in ops.log }
        calls.event(Event.Ended(id, Event.Cause.REJECTED, 480, "client: INVITE rejected"))
        eventually { ends.isNotEmpty() }
        val e = ends.single()
        assertTrue(e.setupFailed)
        assertEquals(480, e.status)
        assertEquals(listOf("denied"), cues.log.toList())
        assertIdle()
    }

    // Ended by the user before an answer: no failure told.
    @Test
    fun leftWhileCalling() {
        val id = requireNotNull(calls.talk(GROUP))
        calls.event(Event.Ended(id, Event.Cause.LOCAL, 0, null))
        eventually { ends.isNotEmpty() }
        assertFalse(ends.single().setupFailed)
        assertTrue(cues.log.isEmpty())
    }

    // P7: the talker's indications play with the courtesy tones off; the floor free does not.
    @Test
    fun mandatoryCues() {
        calls.tones = false
        calls.event(Event.Incoming("in-1", GROUP, "sip:alice@mcptt.example", manual = false))
        calls.event(Event.Established("in-1"))
        calls.event(Event.Floor("in-1", Event.Notice.TAKEN, "sip:bob@mcptt.example"))
        calls.event(Event.Floor("in-1", Event.Notice.IDLE, null))
        calls.event(Event.Floor("in-1", Event.Notice.GRANTED, null))
        // The talk permit once the microphone records, or after its wait.
        eventually { "grant" in cues.log }
        calls.event(Event.Floor("in-1", Event.Notice.REVOKED, null))
        calls.event(Event.Floor("in-1", Event.Notice.TIMEOUT, null))
        eventually { cues.log.size == 3 }
        assertEquals(listOf("grant", "revoked", "revoked"), cues.log.toList())
    }

    // R-6.15.3.2-016: granted before the microphone records, the talk permit waits for it.
    @Test
    fun permitWaitsForMicrophone() {
        audio.slow = true
        val id = requireNotNull(calls.talk(GROUP))
        calls.event(Event.Established(id))
        calls.event(Event.Floor(id, Event.Notice.GRANTED, null))
        eventually { calls.current.value.single().floor == Event.Notice.GRANTED }
        Thread.sleep(100)
        assertTrue(cues.log.isEmpty())
        audio.record()
        eventually { "grant" in cues.log }
    }

    // The microphone not recording after the wait: no talk permit, the floor given back, the press refused.
    @Test
    fun noMicrophone() {
        audio.slow = true
        val id = requireNotNull(calls.talk(GROUP))
        calls.event(Event.Established(id))
        calls.event(Event.Floor(id, Event.Notice.GRANTED, null))
        eventually { "release $id" in ops.log }
        assertEquals(listOf("denied"), cues.log.toList())
    }

    // An ended call's audio stays stopped: the events after its end start no microphone.
    @Test
    fun endedStopsAudio() {
        val id = requireNotNull(calls.talk(GROUP))
        calls.event(Event.Established(id))
        eventually { audio.capturing.get() == 1 }
        calls.event(Event.Ended(id, Event.Cause.LOCAL, 0, null))
        eventually { calls.current.value.isEmpty() }
        Thread.sleep(100)
        assertEquals(0, audio.capturing.get())
        assertEquals(0, audio.playing.get())
    }

    // Each way a call ends leaves nothing running: its microphone, playout, timers and pending talk permit.
    @Test
    fun endedRemotely() {
        val id = active()
        calls.event(Event.Ended(id, Event.Cause.REMOTE, 0, null))
        assertIdle()
    }

    @Test
    fun endedWhileHeld() {
        val id = active()
        runBlocking { telecom.last.onSetInactive() }
        eventually { audio.capturing.get() == 0 }
        calls.event(Event.Ended(id, Event.Cause.REMOTE, 0, null))
        assertIdle()
    }

    @Test
    fun disconnectedByTelecom() {
        val id = active()
        runBlocking { telecom.last.onDisconnect(DisconnectCause(DisconnectCause.LOCAL)) }
        eventually { "leave $id" in ops.log }
        calls.event(Event.Ended(id, Event.Cause.LOCAL, 0, null))
        assertIdle()
    }

    @Test
    fun endedBeforeTheWarning() {
        val id = active()
        calls.event(Event.Floor(id, Event.Notice.GRANTED, null, duration = 6))
        calls.event(Event.Ended(id, Event.Cause.REMOTE, 0, null))
        assertIdle()
        Thread.sleep(1_500)
        assertFalse("warning" in cues.log)
    }

    @Test
    fun endedBeforeTheMicrophone() {
        audio.slow = true
        val id = requireNotNull(calls.talk(GROUP))
        calls.event(Event.Established(id))
        calls.event(Event.Floor(id, Event.Notice.GRANTED, null))
        calls.event(Event.Ended(id, Event.Cause.REMOTE, 0, null))
        assertIdle()
        Thread.sleep(3_500)
        assertTrue(cues.log.isEmpty())
        assertFalse("release $id" in ops.log)
    }

    @Test
    fun manyCalls() {
        repeat(10) {
            val id = active()
            calls.event(Event.Ended(id, if (it % 2 == 0) Event.Cause.LOCAL else Event.Cause.REMOTE, 0, null))
            assertIdle()
        }
    }

    /** A call of the user's, established in the session and active in Telecom. */
    private fun active(): String {
        val id = requireNotNull(calls.talk(GROUP))
        telecom.await()
        calls.event(Event.Established(id))
        eventually { audio.capturing.get() == 1 && audio.playing.get() == 1 }
        return id
    }

    /** No call, and none of their audio running. */
    private fun assertIdle() {
        eventually { calls.current.value.isEmpty() }
        Thread.sleep(100)
        assertEquals("capturing", 0, audio.capturing.get())
        assertEquals("playing", 0, audio.playing.get())
    }

    // A5: the calls in the order they came, the newest last.
    @Test
    fun newestLast() {
        val ids = (1..20).map { "in-$it" }
        for (id in ids) calls.event(Event.Incoming(id, "sip:g$id@mcptt.example", "sip:alice@mcptt.example", manual = true))
        eventually { calls.current.value.size == ids.size }
        assertEquals(ids, calls.current.value.map { it.id })
    }

    private fun eventually(cond: () -> Boolean) {
        val until = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
        while (!cond()) {
            check(System.nanoTime() < until) { "timed out" }
            Thread.sleep(5)
        }
    }

    private class FakeOps : CallOps {
        val log: MutableList<String> = Collections.synchronizedList(mutableListOf())

        @Volatile
        var fail = false

        @Volatile
        var placing: CountDownLatch? = null

        override fun place(id: String, group: String) {
            placing?.await(5, TimeUnit.SECONDS)
            if (fail) error("no session")
            log += "place $id $group"
        }

        override fun accept(id: String) {
            log += "accept $id"
        }

        override fun decline(id: String) {
            log += "decline $id"
        }

        override fun leave(id: String) {
            log += "leave $id"
        }

        override fun press(id: String) {
            log += "press $id"
        }

        override fun release(id: String) {
            log += "release $id"
        }

        override fun nextSpeech(id: String) = ByteArray(1)

        override fun sendSpeech(id: String, frame: ByteArray) = Unit
    }

    private class FakeCues : Cues {
        val log: MutableList<String> = Collections.synchronizedList(mutableListOf())

        override fun grant() {
            log += "grant"
        }

        override fun denied() {
            log += "denied"
        }

        override fun free() {
            log += "free"
        }

        override fun warning() {
            log += "warning"
        }

        override fun revoked() {
            log += "revoked"
        }

        override fun lost() {
            log += "lost"
        }
    }

    /** The calls' audio: how many play. */
    private class FakeAudio : Audio {
        val playing = AtomicInteger()
        val capturing = AtomicInteger()

        /** The microphone slow: its readiness told by [record], not at its start. */
        @Volatile
        var slow = false
        private val pending = LinkedBlockingQueue<() -> Unit>()

        fun record() = requireNotNull(pending.poll(5, TimeUnit.SECONDS)) { "no capture started" }()

        override fun playout(next: () -> ByteArray, failed: (String) -> Unit): SpeechOut = object : SpeechOut {
            private var on = false

            override fun start() {
                if (!on) playing.incrementAndGet()
                on = true
            }

            override fun stop() {
                if (on) playing.decrementAndGet()
                on = false
            }
        }

        override fun capture(send: (ByteArray) -> Unit, failed: (String) -> Unit, ready: () -> Unit): SpeechIn = object : SpeechIn {
            override var ready = false

            private var on = false

            override fun start(mode: Int) {
                if (!on) capturing.incrementAndGet()
                on = true
                if (this.ready) return
                val recording = {
                    this.ready = true
                    ready()
                }
                if (slow) pending += recording else recording()
            }

            override fun stop() {
                if (on) capturing.decrementAndGet()
                on = false
                this.ready = false
            }

            override fun send(on: Boolean) = Unit

            override fun mode(mode: Int) = Unit
        }
    }

    class Added(
        val attributes: CallAttributesCompat,
        val onAnswer: suspend (Int) -> Unit,
        val onDisconnect: suspend (DisconnectCause) -> Unit,
        val onSetInactive: suspend () -> Unit,
        val control: FakeControl,
    )

    private class FakeTelecom : Telecom {
        val added = LinkedBlockingQueue<Added>()

        @Volatile
        lateinit var last: Added

        fun await(): Added = requireNotNull(added.poll(5, TimeUnit.SECONDS)) { "no call added" }.also { last = it }

        override suspend fun addCall(
            attributes: CallAttributesCompat,
            onAnswer: suspend (callType: Int) -> Unit,
            onDisconnect: suspend (cause: DisconnectCause) -> Unit,
            onSetActive: suspend () -> Unit,
            onSetInactive: suspend () -> Unit,
            block: CallControlScope.() -> Unit,
        ) = coroutineScope {
            val control = FakeControl(coroutineContext)
            added += Added(attributes, onAnswer, onDisconnect, onSetInactive, control)
            control.block()
        }
    }

    class FakeControl(override val coroutineContext: CoroutineContext) : CallControlScope {
        private val id = ParcelUuid(UUID.randomUUID())

        override fun getCallId() = id

        override suspend fun setActive(): CallControlResult = CallControlResult.Success()

        override suspend fun setInactive(): CallControlResult = CallControlResult.Success()

        override suspend fun answer(callType: Int): CallControlResult = CallControlResult.Success()

        override suspend fun disconnect(disconnectCause: DisconnectCause): CallControlResult = CallControlResult.Success()

        override suspend fun requestEndpointChange(endpoint: CallEndpointCompat): CallControlResult = CallControlResult.Success()

        override val currentCallEndpoint: Flow<CallEndpointCompat> = MutableSharedFlow()
        override val availableEndpoints: Flow<List<CallEndpointCompat>> = MutableStateFlow(emptyList())
        override val isMuted: Flow<Boolean> = MutableStateFlow(false)
    }
}
