package com.ellanetworks.mcptt.service

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.Person
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import com.ellanetworks.mcptt.McpttApp
import com.ellanetworks.mcptt.R
import com.ellanetworks.mcptt.Status
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.call.Ended
import com.ellanetworks.mcptt.call.PttKeys
import com.ellanetworks.mcptt.call.groupName
import com.ellanetworks.mcptt.session.Event
import com.ellanetworks.mcptt.ui.MainActivity
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch

/**
 * The MCPTT service in the foreground: the session runs while it does, registered to be reached for calls with the app
 * in the background and the device asleep.
 *
 * Its types: specialUse always, the standby of no call (no other type covers a client kept registered without push,
 * and specialUse may start at boot: Android 15 bans phoneCall and microphone there); microphone once the app has
 * been visible, or the user has answered from the notification (the while-in-use rule's moments), kept while the
 * service runs so that the user talks with the app in the background (decision 13); phoneCall while a call lasts.
 * Its notification is the status, or the call's (CallStyle) during a call.
 */
class SessionService : LifecycleService() {
    private val app: McpttApp
        get() = application as McpttApp

    /** The service holds the microphone type: the process captures in the background. */
    private var microphone = false
    private var last: Notification? = null
    private var inCall = false

    /** The selected group, of the settings: the PTT key's. */
    @Volatile
    private var group: String? = null
    private val keys by lazy { PttKeys(this, app.sessions.ptt) { group } }

    override fun onCreate() {
        super.onCreate()
        running = true
        channels()
        foreground(status(app.sessions.status.value, null))
        // Made while the app is visible: its moment.
        if (visible) microphone()
        app.sessions.start()
        lifecycleScope.launch { app.sessions.calls.ended.collect(::ended) }
        lifecycleScope.launch { app.settings.data.collect { group = it.group } }
        keys.start()
        lifecycleScope.launch {
            combine(app.sessions.status, app.sessions.calls.current, app.settings.data) { s, c, u -> shown(s, c, u.group) }
                .distinctUntilChanged()
                .collect { (s, call, group) ->
                    val n = if (call != null) call(call) else status(s, group)
                    if (inCall != (call != null)) {
                        inCall = call != null
                        foreground(n)
                    } else {
                        notify(n)
                    }
                }
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        val id = intent?.getStringExtra(EXTRA_CALL)
        when (intent?.action) {
            ACTION_VISIBLE -> microphone()

            ACTION_ANSWER -> {
                microphone()
                id?.let(app.sessions.calls::accept)
            }

            ACTION_DECLINE -> id?.let(app.sessions.calls::decline)

            ACTION_LEAVE -> id?.let(app.sessions.calls::leave)
        }
        // Restarted by the system if it kills the process: the user wants the service until they stop it.
        return START_STICKY
    }

    override fun onDestroy() {
        running = false
        keys.stop()
        app.sessions.microphone.value = false
        app.sessions.stop()
        super.onDestroy()
    }

    /** At a moment of the while-in-use rule: the microphone type added, if the user allows the app to record. */
    private fun microphone() {
        if (microphone) return
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) return
        microphone = true
        foreground(last ?: status(app.sessions.status.value, null))
    }

    /**
     * In the foreground of the types of now. A type the system refuses, of the microphone (the while-in-use rule) or a
     * call's, is left out this time: the microphone asked for again at the next change, its grant kept by the system
     * once given to the running service.
     */
    private fun foreground(n: Notification) {
        last = n
        val base = ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
        val call = if (inCall) ServiceInfo.FOREGROUND_SERVICE_TYPE_PHONE_CALL else 0
        val mic = if (microphone) ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE else 0
        val held = listOf(base or call or mic, base or call, base).distinct().firstOrNull { types ->
            try {
                ServiceCompat.startForeground(this, NOTIFICATION_ID, n, types)
                true
            } catch (e: SecurityException) {
                Log.w(TAG, "foreground types $types refused: $e")
                false
            } catch (e: IllegalStateException) {
                Log.w(TAG, "foreground types $types refused: $e")
                false
            }
        }
        app.sessions.microphone.value = held != null && held and ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE != 0
        if (held == null) {
            // Not in the foreground, of even specialUse: stopped, before the system's ForegroundServiceDidNotStartInTime.
            Log.e(TAG, "foreground refused: stopping")
            stopSelf()
        }
    }

    private fun notify(n: Notification) {
        last = n
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, n)
    }

    private fun channels() {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.channel_service), NotificationManager.IMPORTANCE_LOW).apply {
                description = getString(R.string.channel_service_description)
            },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_CALLS, getString(R.string.channel_calls), NotificationManager.IMPORTANCE_HIGH).apply {
                description = getString(R.string.channel_calls_description)
            },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_MISSED, getString(R.string.channel_missed), NotificationManager.IMPORTANCE_DEFAULT).apply {
                description = getString(R.string.channel_missed_description)
            },
        )
    }

    private fun open(): PendingIntent = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)

    /** The status notification: the registration, and the selected group once registered. */
    private fun status(status: Status, group: String?): Notification = NotificationCompat.Builder(this, CHANNEL)
        .setSmallIcon(R.drawable.ic_notification)
        .setContentTitle(getString(R.string.app_name))
        .setContentText(
            if (status is Status.Registered && group != null) {
                getString(R.string.status_group, getString(status.text()), groupName(group))
            } else {
                getString(status.text())
            },
        )
        .setContentIntent(open())
        .setOngoing(true)
        .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
        .setCategory(NotificationCompat.CATEGORY_SERVICE)
        .build()

    /** The call's notification: an incoming call's to answer or decline, over the lock screen; an ongoing one's. */
    private fun call(c: CallInfo): Notification {
        val group = Person.Builder().setName(groupName(c.group)).setUri(c.group).setImportant(true).build()
        val style = if (c.state == CallInfo.State.RINGING) {
            NotificationCompat.CallStyle.forIncomingCall(group, action(ACTION_DECLINE, c.id), action(ACTION_ANSWER, c.id))
        } else {
            NotificationCompat.CallStyle.forOngoingCall(group, action(ACTION_LEAVE, c.id))
        }
        val b = NotificationCompat.Builder(this, CHANNEL_CALLS)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentIntent(open())
            .setOngoing(true)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setStyle(style)
            .setContentText(callText(c))
        if (c.state == CallInfo.State.RINGING) b.setFullScreenIntent(open(), true) else b.setSilent(true)
        return b.build()
    }

    /** A call missed (an invitation not answered) or lost (the network ended it): a notification of its own. */
    private fun ended(e: Ended) {
        val (title, text) = when {
            e.cause == Event.Cause.MISSED ->
                getString(R.string.missed_call, groupName(e.call.group)) to e.call.caller?.let { getString(R.string.missed_from, groupName(it)) }

            e.cause == Event.Cause.ERROR && e.call.state == CallInfo.State.ACTIVE ->
                getString(R.string.call_lost, groupName(e.call.group)) to getString(R.string.call_lost_detail)

            else -> return
        }
        val n = NotificationCompat.Builder(this, CHANNEL_MISSED)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open())
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_MISSED_CALL)
            .build()
        getSystemService(NotificationManager::class.java).notify(e.call.id.hashCode(), n)
    }

    private fun callText(c: CallInfo): String = when {
        c.state == CallInfo.State.RINGING -> getString(R.string.call_incoming, c.caller?.let(::groupName).orEmpty())
        c.state == CallInfo.State.CALLING -> getString(R.string.call_calling)
        c.state == CallInfo.State.HELD -> getString(R.string.call_held)
        c.transmitting -> getString(R.string.call_talking)
        c.talker != null -> getString(R.string.call_talker, groupName(c.talker))
        else -> getString(R.string.call_idle)
    }

    private fun action(action: String, id: String): PendingIntent = PendingIntent.getService(
        this,
        // One of each action and call: of the action alone, the calls' extras would replace each other's.
        (action + id).hashCode(),
        Intent(this, SessionService::class.java).setAction(action).putExtra(EXTRA_CALL, id),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    companion object {
        private const val TAG = "EllaMcptt"
        private const val CHANNEL = "service"
        private const val CHANNEL_CALLS = "calls"
        private const val CHANNEL_MISSED = "missed"
        private const val NOTIFICATION_ID = 1
        private const val ACTION_VISIBLE = "com.ellanetworks.mcptt.VISIBLE"
        private const val ACTION_ANSWER = "com.ellanetworks.mcptt.ANSWER"
        private const val ACTION_DECLINE = "com.ellanetworks.mcptt.DECLINE"
        private const val ACTION_LEAVE = "com.ellanetworks.mcptt.LEAVE"
        private const val EXTRA_CALL = "call"

        @Volatile
        private var running = false

        @Volatile
        private var visible = false

        /** Starts the service: at boot, or from the UI, [visible] (a moment of the while-in-use rule). */
        fun start(context: Context, visible: Boolean = false) {
            val i = Intent(context, SessionService::class.java)
            if (visible) i.action = ACTION_VISIBLE
            ContextCompat.startForegroundService(context, i)
        }

        /** Stops the service, and the session with it: not started if not running (startService would). */
        fun stop(context: Context) {
            context.stopService(Intent(context, SessionService::class.java))
        }

        /**
         * The app is visible, or no more: a moment of the while-in-use rule, the service taking the microphone type,
         * now if it runs, else when it starts.
         */
        fun visible(context: Context, now: Boolean) {
            visible = now
            if (now && running) context.startService(Intent(context, SessionService::class.java).setAction(ACTION_VISIBLE))
        }
    }
}

/** What the service's notification shows: the status, or the [call]'s, and the selected group. */
internal data class Shown(val status: Status, val call: CallInfo?, val group: String?)

/**
 * What the notification shows of [status], [calls] and [group]: the call to answer first, else the active one, else one
 * calling, else one held; of each, the newest. Of the call, what its notification shows alone, so that a change of
 * route or talk time leaves the notification as it is (the system limits an app's rate of updates, dropping some).
 */
internal fun shown(status: Status, calls: List<CallInfo>, group: String?): Shown {
    val call = listOf(CallInfo.State.RINGING, CallInfo.State.ACTIVE, CallInfo.State.CALLING, CallInfo.State.HELD)
        .firstNotNullOfOrNull { state -> calls.lastOrNull { it.state == state } }
    val shown = call?.let {
        CallInfo(id = it.id, group = it.group, caller = it.caller, state = it.state, talker = it.talker, transmitting = it.transmitting)
    }
    return Shown(status, shown, group)
}

/** The status of the notification. */
fun Status.text(): Int = when (this) {
    Status.Stopped -> R.string.status_stopped
    is Status.Unconfigured -> R.string.status_unconfigured
    Status.NoNetwork -> R.string.status_no_network
    Status.Registering -> R.string.status_registering
    is Status.Registered -> R.string.status_registered
    is Status.Failed -> R.string.status_failed
    Status.Rejected -> R.string.status_rejected
}
