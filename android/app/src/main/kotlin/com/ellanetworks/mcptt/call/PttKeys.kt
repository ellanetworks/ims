package com.ellanetworks.mcptt.call

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.util.Log
import androidx.core.content.ContextCompat

class PttKeys(private val context: Context, private val ptt: PttController, private val group: () -> String?) {
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
