package com.ellanetworks.mcptt.call

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.util.Log
import androidx.core.content.ContextCompat

/**
 * A device's PTT key, as its vendor broadcasts it to the app chosen for it: the Crosscall Core-Z5's (Settings →
 * Buttons → the button's long press → "Push to Talk application"), FUNCTION_KEY_DOWN_PRESSED at the key's down and
 * FUNCTION_KEY_RELEASED at its up, sent by the system with the screen off too (android_ptt_keys.md). Held, it talks to
 * the selected group ([group]), as the screen's PTT does.
 */
class PttKeys(private val context: Context, private val ptt: PttController, private val group: () -> String?) {
    /** The key down. */
    private var down = false

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            when (intent.action) {
                DOWN -> down()
                UP -> up()
            }
        }
    }

    fun start() {
        val filter = IntentFilter().apply {
            addAction(DOWN)
            addAction(UP)
        }
        // Not exported: the system sends them, which reaches a receiver of any app; another app may not.
        ContextCompat.registerReceiver(context, receiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
    }

    fun stop() {
        runCatching { context.unregisterReceiver(receiver) }
        up()
    }

    private fun down() {
        if (down) return
        val g = group() ?: return
        down = true
        Log.i(TAG, "PTT key down: ${ptt.press(PttController.Source.KEY, g) ?: "refused"}")
    }

    private fun up() {
        if (!down) return
        down = false
        ptt.release(PttController.Source.KEY)
        Log.i(TAG, "PTT key up")
    }

    private companion object {
        const val TAG = "EllaMcptt"
        const val DOWN = "android.intent.action.FUNCTION_KEY_DOWN_PRESSED"
        const val UP = "android.intent.action.FUNCTION_KEY_RELEASED"
    }
}
