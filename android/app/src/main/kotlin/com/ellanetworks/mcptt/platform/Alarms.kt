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

class Alarms(private val context: Context) {
    private val alarms = context.getSystemService(AlarmManager::class.java)
    private val lock = context.getSystemService(PowerManager::class.java)
        .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "ellamcptt:session")
        .apply { setReferenceCounted(false) }
    private var awakeUntil = 0L

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
        Intent(context, AlarmReceiver::class.java).setAction(ACTION).setData(Uri.fromParts(SCHEME, id.toString(), null)),
        flags or PendingIntent.FLAG_IMMUTABLE,
    )

    companion object {
        const val ACTION = "com.ellanetworks.mcptt.ALARM"
        const val SCHEME = "mcptt-alarm"
    }
}

class AlarmReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Alarms.ACTION) return
        val id = intent.data?.schemeSpecificPart?.toLongOrNull() ?: return
        (context.applicationContext as McpttApp).sessions.woken(id)
    }
}
