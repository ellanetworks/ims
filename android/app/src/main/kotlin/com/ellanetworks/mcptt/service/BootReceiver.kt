package com.ellanetworks.mcptt.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.ellanetworks.mcptt.McpttApp
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking

/** Starts the service when the device boots, or the app is updated, unless the user turned it off (decision 2). */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val app = context.applicationContext as McpttApp
        // A small file, read once: well within the receiver's time.
        // A store unreadable (an I/O error; a corrupt file is replaced): started, as by default, to show the failure.
        val start = runCatching { runBlocking { app.settings.data.first() }.startOnBoot }.getOrDefault(true)
        if (start) SessionService.start(context)
    }
}
