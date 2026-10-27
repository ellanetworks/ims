package com.ellanetworks.mcptt

import android.content.Context
import android.telephony.SubscriptionManager
import android.telephony.TelephonyManager
import android.util.Log
import androidx.datastore.core.DataStore
import com.ellanetworks.mcptt.call.CallOps
import com.ellanetworks.mcptt.call.Calls
import com.ellanetworks.mcptt.call.PttController
import com.ellanetworks.mcptt.go.mobile.Mobile
import com.ellanetworks.mcptt.go.mobile.Session
import com.ellanetworks.mcptt.media.AmrWb
import com.ellanetworks.mcptt.platform.AccessNetwork
import com.ellanetworks.mcptt.platform.Alarms
import com.ellanetworks.mcptt.platform.AndroidPlatform
import com.ellanetworks.mcptt.platform.Cellular
import com.ellanetworks.mcptt.platform.CellularNetwork
import com.ellanetworks.mcptt.platform.Identities
import com.ellanetworks.mcptt.platform.Ipsec
import com.ellanetworks.mcptt.platform.Isim
import com.ellanetworks.mcptt.session.Event
import com.ellanetworks.mcptt.session.Settings
import com.ellanetworks.mcptt.settings.UserSettings
import com.ellanetworks.mcptt.settings.withClientId
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerializationException
import java.net.Inet6Address
import java.net.InetAddress

/** The state of the app's session, as the UI and the notification show it. */
sealed interface Status {
    data object Stopped : Status

    /** Settings the user must still set. */
    data class Unconfigured(val missing: List<String>) : Status

    data object NoNetwork : Status

    data object Registering : Status

    data class Registered(val impu: String) : Status

    /** The last registration failed: the session tries again. */
    data class Failed(val error: String) : Status

    /** The network refused the user (TS 24.229 §5.1.1.7): the session registers no more. */
    data object Rejected : Status
}

/** A line of the session's log: its level of log/slog (-4 debug, 0 info, 4 warning, 8 error). */
data class LogLine(val level: Int, val text: String)

/**
 * The MCPTT session of the app, one in the process: built of the ISIM, the user's settings and the cellular network
 * when started, rebuilt when the settings change, told of each change of the network (Session.SetNetwork).
 * SessionService runs it in the foreground; the alarms, the calls and the UI reach it here.
 */
class Sessions(private val context: Context, private val store: DataStore<UserSettings>) {
    // An exception escaped of the session's coroutines: failed, not the process crashed (START_STICKY and the start at
    // boot would make that a loop).
    private val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Default + CoroutineExceptionHandler { _, e -> fail(e) },
    )
    private val tm = context.getSystemService(TelephonyManager::class.java)
        .createForSubscriptionId(SubscriptionManager.getDefaultDataSubscriptionId())
    private val network = CellularNetwork(context)
    private val accessNetwork = AccessNetwork(context, tm)
    private val ipsec = Ipsec(context)
    private val alarms = Alarms(context)

    private val state = MutableStateFlow<Status>(Status.Stopped)
    private val lines = MutableStateFlow<List<LogLine>>(emptyList())

    val status: StateFlow<Status> = state.asStateFlow()
    val log: StateFlow<List<LogLine>> = lines.asStateFlow()

    /** The session's log at debug: a field engineer's switch, off again when the app restarts. */
    val debug = MutableStateFlow(false)

    fun setDebug(on: Boolean) {
        debug.value = on
        session?.setLogLevel(if (on) "debug" else "info")
    }

    /** Empties the log of the screen; logcat keeps its own. */
    fun clearLog() {
        lines.value = emptyList()
    }

    /** SessionService holds the microphone type: a call's PTT works with the app in the background (decision 13). */
    val microphone = MutableStateFlow(false)

    /** The app is visible: it may record without the microphone type (the while-in-use rule). */
    val visible = MutableStateFlow(false)

    @Volatile
    private var session: Session? = null

    /** The session's generation, one more at each made and closed: the events of a closed one are dropped. */
    @Volatile
    private var generation = 0L

    /** The status of the session's registration, as its events tell, and whether it has a network: [shown] of both. */
    @Volatile
    private var registration: Status = Status.Registering

    @Volatile
    private var online = false

    /** The handle, address and P-CSCF the session was last told of. */
    @Volatile
    private var told: Triple<String, String?, String>? = null

    @Volatile
    private var mcpttId: String? = null

    private val runs = Runs(scope, ::running)

    /** The calls of the session, through Telecom. */
    val calls = Calls(context, Ops(), self = { mcpttId }, recordable = { microphone.value || visible.value })

    /** The PTT, of the screen's button and the device's key. */
    val ptt = PttController(calls::talk, calls::release) { id -> calls.current.value.any { it.id == id } }

    init {
        // The user's tone setting, with no restart of the session.
        scope.launch {
            store.data.catch { onLog(LEVEL_ERROR, "settings: $it") }.collect { calls.tones = it.tones }
        }
    }

    /** The session's calls by ID; without a session, none. */
    private inner class Ops : CallOps {
        private fun s() = session ?: error("no session")

        override fun place(id: String, group: String) = s().call(id, group)
        override fun accept(id: String) = quietly { it.accept(id) }
        override fun decline(id: String) = quietly { it.decline(id) }
        override fun leave(id: String) = quietly { it.leave(id) }
        override fun press(id: String) = quietly { it.pressPTT(id) }
        override fun release(id: String) = quietly { it.releasePTT(id) }
        override fun nextSpeech(id: String): ByteArray = session?.nextSpeech(id) ?: AmrWb.NO_DATA
        override fun sendSpeech(id: String, frame: ByteArray) = quietly { it.sendSpeech(id, frame) }

        // A call gone, or the session: nothing to do.
        private fun quietly(f: (Session) -> Unit) {
            val s = session ?: return
            try {
                f(s)
            } catch (_: Exception) {
            }
        }
    }

    /** Starts the session, unless running: it registers once configured and on a network. */
    fun start() = runs.start()

    /** Stops the session: it deregisters, which may take seconds; a start meanwhile waits for it. */
    fun stop() = runs.stop()

    /** Whether the app has carrier privileges on the session's subscription (the ISIM's, and AKA's). */
    fun hasCarrierPrivileges(): Boolean = tm.hasCarrierPrivileges()

    /** An alarm of Platform.Wake gone off. */
    fun woken(id: Long) {
        session?.woken(id)
    }

    /** A run of the session, from its start to its stop: its watchers of the network registered, then released. */
    private suspend fun running() {
        try {
            network.start()
            accessNetwork.start()
            run()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            fail(e)
            awaitCancellation()
        } finally {
            withContext(NonCancellable + Dispatchers.IO) {
                close()
                network.stop()
                accessNetwork.stop()
                state.value = Status.Stopped
            }
        }
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    private suspend fun run() {
        store.withClientId()
        store.data.map { it.session() }.distinctUntilChanged().collectLatest { user ->
            withContext(Dispatchers.IO) { close() }
            if (user.missing.isNotEmpty()) {
                state.value = Status.Unconfigured(user.missing)
                return@collectLatest
            }
            // Without carrier privileges, the ISIM's reads throw (SecurityException): the session waits for another
            // start, or other settings.
            if (!tm.hasCarrierPrivileges()) {
                state.value = Status.Failed(NO_CARRIER_PRIVILEGES)
                return@collectLatest
            }
            val isim = Isim(tm)
            val (ids, imei) = try {
                withContext(Dispatchers.IO) { isim.identities() to isim.imei() }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                fail(e)
                return@collectLatest
            }
            val impu = ids.mcpttImpu ?: run {
                state.value = Status.Failed(NO_MCPTT_IDENTITY)
                return@collectLatest
            }
            combine(network.current, accessNetwork.current) { n, ani -> n to ani }
                .distinctUntilChanged()
                .collectLatest { (n, ani) ->
                    try {
                        withContext(Dispatchers.IO) { onNetwork(user, ids, impu, imei, n, ani) }
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        // Another network, or other settings, tries again.
                        fail(e)
                    }
                }
        }
    }

    /** Failed, of an exception of the platform or the session: logged, and shown. */
    private fun fail(e: Throwable) {
        onLog(LEVEL_ERROR, "session: $e")
        val privileges = e is SecurityException && !runCatching { tm.hasCarrierPrivileges() }.getOrDefault(false)
        state.value = Status.Failed(if (privileges) NO_CARRIER_PRIVILEGES else e.message ?: e.toString())
    }

    /** The registration the session's events tell of, shown with the network's state. */
    @Synchronized
    private fun publish(registration: Status = this.registration, online: Boolean = this.online) {
        this.registration = registration
        this.online = online
        state.value = shown(registration, online)
    }

    /**
     * The session on the network [n]: made on the first, told of the next. Without one, the session waits, shown with
     * no network: Go learns of the next (SetNetwork, its sockets new), its retransmissions failing meanwhile.
     */
    private fun onNetwork(user: UserSettings, ids: Identities, impu: String, imei: String, n: Cellular?, ani: String) {
        if (n == null) {
            if (session == null) state.value = Status.NoNetwork else publish(online = false)
            return
        }
        val pcscf = pcscf(user, ids, n) ?: run {
            state.value = Status.Failed("no P-CSCF: set one, or provision the ISIM's")
            return
        }
        val local = n.local(pcscf.address) ?: run {
            state.value = Status.Failed("no address of the P-CSCF's family on the cellular network")
            return
        }
        val handle = n.network.networkHandle.toString()
        val s = session
        if (s != null) {
            // Rejected, the session registers no more (SetNetwork refuses it).
            if (registration == Status.Rejected) return
            val now = Triple(handle, local.hostAddress, pcscf.text)
            try {
                s.setNetwork(handle, local.hostAddress, pcscf.text, ani)
                // Another network: a new registration (Session.SetNetwork).
                publish(if (now != told) Status.Registering else registration, online = true)
                told = now
                return
            } catch (e: Exception) {
                // The session closed of a network it could not use: a new one made of it.
                onLog(LEVEL_ERROR, "set network: $e")
                close()
            }
        }
        val settings = Settings(
            impi = ids.impi, impu = impu, homeDomain = ids.homeDomain,
            imei = imei,
            pcscf = pcscf.text, network = handle, local = requireNotNull(local.hostAddress), accessNetworkInfo = ani,
            mcpttId = user.mcpttId, clientId = user.clientId, participatingPsi = user.participatingPsi,
            groups = user.groups, logLevel = if (debug.value) Settings.LogLevel.DEBUG else Settings.LogLevel.INFO,
        )
        val gen = ++generation
        val platform = AndroidPlatform(
            network = { network.current.value },
            isim = Isim(tm),
            hasIsim = ids.isim,
            ipsec = ipsec,
            alarms = alarms,
            onEvent = { onEvent(gen, it) },
            onLog = ::onLog,
        )
        publish(Status.Registering, online = true)
        told = Triple(handle, local.hostAddress, pcscf.text)
        mcpttId = user.mcpttId
        try {
            session = Mobile.newSession(platform, settings.toJson()).also { it.start() }
        } catch (e: Exception) {
            state.value = Status.Failed(e.message ?: e.toString())
        }
    }

    /** The P-CSCF of the user's settings, else the ISIM's first (TS 24.229 §9.2.1), resolved on the network. */
    private fun pcscf(user: UserSettings, ids: Identities, n: Cellular): Pcscf? {
        val configured = user.pcscf.ifBlank { ids.pcscfs.firstOrNull() } ?: return null
        val (host, port) = hostPort(configured) ?: return null
        val addr = runCatching { n.network.getAllByName(host).first() }.getOrNull() ?: return null
        return Pcscf(addr, port)
    }

    private data class Pcscf(val address: InetAddress, val port: Int) {
        val text: String
            get() = if (address is Inet6Address) "[${address.hostAddress}]:$port" else "${address.hostAddress}:$port"
    }

    private fun close() {
        val s = session ?: return
        session = null
        generation++
        told = null
        registration = Status.Registering
        calls.clear()
        // Deregisters: seconds at most (the session's stopTimeout).
        s.stop()
    }

    /** An event of the session of generation [gen]: of one closed, logged alone (its calls ended, the status another's). */
    private fun onEvent(gen: Long, text: String) {
        val e = try {
            Event.decode(text)
        } catch (e: SerializationException) {
            onLog(LEVEL_ERROR, "event $text: ${e.message}")
            return
        }
        onLog(LEVEL_INFO, "event $e")
        if (gen != generation) return
        when (e) {
            is Event.Registered -> publish(Status.Registered(e.impu))
            is Event.RegistrationFailed -> publish(Status.Failed(e.error))
            is Event.Deregistered -> publish(Status.Registering)
            is Event.Stopped -> if (e.reason == Event.StopReason.REJECTED) publish(Status.Rejected)
            is Event.OfCall -> calls.event(e)
            is Event.Affiliation -> Unit
        }
    }

    private fun onLog(level: Int, text: String) {
        val priority = when {
            level >= LEVEL_ERROR -> Log.ERROR
            level >= LEVEL_WARN -> Log.WARN
            level >= LEVEL_INFO -> Log.INFO
            else -> Log.DEBUG
        }
        Log.println(priority, TAG, text)
        lines.update { (it + LogLine(level, text)).takeLast(MAX_LINES) }
    }

    private companion object {
        const val TAG = "EllaMcptt"
        const val MAX_LINES = 500
        const val LEVEL_INFO = 0
        const val LEVEL_WARN = 4
        const val LEVEL_ERROR = 8
        const val NO_CARRIER_PRIVILEGES = "no carrier privileges: the SIM's rules (ARA-M) lack the app's certificate"
        const val NO_MCPTT_IDENTITY = "no MCPTT identity: the ISIM's EF_IMPU lacks sip:mcptt-<MSISDN>@<domain>"
    }
}

/** The status shown: the registration's, or no network while the session has none; a rejection shown regardless. */
internal fun shown(registration: Status, online: Boolean): Status = if (online || registration == Status.Rejected) registration else Status.NoNetwork

/**
 * Runs [body] while started, one run at a time: a start waits for the last run's end, its teardown in [body]'s
 * finally, so that a stop then a start never overlap (the network callbacks registered twice, or the new run's
 * released by the old's teardown).
 */
internal class Runs(private val scope: CoroutineScope, private val body: suspend () -> Unit) {
    private var job: Job? = null
    private var last: Job? = null

    /** Starts a run, unless one is: after the last's end. */
    @Synchronized
    fun start() {
        if (job != null) return
        val previous = last
        // Ends with this run and the ones before: of a run stopped before it began, too.
        val done = Job()
        last = done
        job = scope.launch {
            withContext(NonCancellable) { previous?.join() }
            ensureActive()
            body()
        }.apply {
            invokeOnCompletion { if (previous == null) done.complete() else previous.invokeOnCompletion { done.complete() } }
        }
    }

    /** Stops the run, its teardown ending in its own time. */
    @Synchronized
    fun stop() {
        job?.cancel()
        job = null
    }
}

private const val SIP_PORT = 5060

/** A host and port: host, host:port, v6, [v6] or [v6]:port; the SIP port by default (RFC 3261 §19.1.2). */
internal fun hostPort(s: String): Pair<String, Int>? {
    val t = s.trim()
    Regex("""\[([^\]]+)](?::(\d{1,5}))?""").matchEntire(t)?.let {
        return it.groupValues[1] to (it.groupValues[2].toIntOrNull() ?: SIP_PORT)
    }
    if (t.count { it == ':' } > 1) return t to SIP_PORT
    val parts = t.split(":")
    val port = if (parts.size == 2) parts[1].toIntOrNull()?.takeIf { it in 1..65535 } ?: return null else SIP_PORT
    return parts[0].takeIf(String::isNotEmpty)?.let { it to port }
}
