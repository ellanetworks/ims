package com.ellanetworks.mcptt.service

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.ellanetworks.mcptt.McpttApp
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val app = context.applicationContext as McpttApp
        val start = runCatching { runBlocking { app.settings.data.first() }.startOnBoot }.getOrDefault(true)
        if (start) SessionService.start(context)
    }
}
