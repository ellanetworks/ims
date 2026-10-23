package com.ellanetworks.mcptt.platform

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.os.SystemClock
import com.ellanetworks.mcptt.McpttApp

/**
 * The device's alarms and wake locks, for the session's long timers (Platform.Wake): its registration's and
 * subscriptions' refreshes, which must fire while the device sleeps. Exact while idle when the app may schedule exact
 * alarms: of the permission, or exempt from battery optimisation, whose alarms are also unrestricted in Doze (72 an
 * hour otherwise); without, as close as the system allows (7 an hour in Doze), the session's timers then late.
 */
class Alarms(private val context: Context) {
    private val alarms = context.getSystemService(AlarmManager::class.java)
    private val lock = context.getSystemService(PowerManager::class.java)
        .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "ellamcptt:session")
        .apply { setReferenceCounted(false) }
    private var awakeUntil = 0L

    /** Wakes the device in [ms] to fire alarm [id]: AlarmReceiver gives it to the session. */
    fun wake(id: Long, ms: Long) {
        val at = SystemClock.elapsedRealtime() + ms
        val pi = intent(id, PendingIntent.FLAG_UPDATE_CURRENT) ?: return
        if (alarms.canScheduleExactAlarms()) {
            alarms.setExactAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME_WAKEUP, at, pi)
        } else {
            alarms.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME_WAKEUP, at, pi)
        }
    }

    fun cancel(id: Long) {
        intent(id, PendingIntent.FLAG_NO_CREATE)?.let {
            alarms.cancel(it)
            it.cancel()
        }
    }

    /** Keeps the CPU awake [ms] more, a later deadline extending the wake lock, never shortening it. */
    @Synchronized
    fun keepAwake(ms: Long) {
        val until = SystemClock.elapsedRealtime() + ms
        if (lock.isHeld && until <= awakeUntil) return
        awakeUntil = until
        lock.acquire(ms)
    }

    private fun intent(id: Long, flags: Int): PendingIntent? = PendingIntent.getBroadcast(
        context,
        0,
        // The ID in the data, so that each alarm is a PendingIntent of its own.
        Intent(context, AlarmReceiver::class.java).setAction(ACTION).setData(Uri.fromParts(SCHEME, id.toString(), null)),
        flags or PendingIntent.FLAG_IMMUTABLE,
    )

    companion object {
        const val ACTION = "com.ellanetworks.mcptt.ALARM"
        const val SCHEME = "mcptt-alarm"
    }
}

/** An alarm of the session gone off: Session.Woken, which holds a wake lock for the work it starts, and returns. */
class AlarmReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Alarms.ACTION) return
        val id = intent.data?.schemeSpecificPart?.toLongOrNull() ?: return
        (context.applicationContext as McpttApp).sessions.woken(id)
    }
}
