package com.ellanetworks.mcptt.ui

import android.Manifest
import android.app.AlarmManager
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LifecycleResumeEffect
import com.ellanetworks.mcptt.R

data class Need(val text: Int, val action: (() -> Unit)?, val blocking: Boolean = false)

@Composable
fun rememberNeeds(vm: AppViewModel): List<Need> {
    val context = LocalContext.current
    var tick by remember { mutableIntStateOf(0) }
    LifecycleResumeEffect(Unit) {
        tick++
        onPauseOrDispose {}
    }
    val notifications = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { tick++ }
    val microphone = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) {
        tick++
        if (it) vm.visible()
    }
    return remember(tick) {
        buildList {
            if (!vm.hasCarrierPrivileges()) {
                add(Need(R.string.need_carrier_privileges, null, blocking = true))
            }
            if (!context.granted(Manifest.permission.RECORD_AUDIO)) {
                add(Need(R.string.need_microphone, { microphone.launch(Manifest.permission.RECORD_AUDIO) }))
            }
            if (!context.granted(Manifest.permission.POST_NOTIFICATIONS)) {
                add(Need(R.string.need_notifications, { notifications.launch(Manifest.permission.POST_NOTIFICATIONS) }))
            }
            if (!context.getSystemService(NotificationManager::class.java).canUseFullScreenIntent()) {
                add(Need(R.string.need_full_screen, { context.open(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT) }))
            }
            if (!context.getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(context.packageName)) {
                @Suppress("BatteryLife")
                add(Need(R.string.need_battery, { context.open(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS) }))
            } else if (!context.getSystemService(AlarmManager::class.java).canScheduleExactAlarms()) {
                add(Need(R.string.need_exact_alarms, { context.open(Settings.ACTION_REQUEST_SCHEDULE_EXACT_ALARM) }))
            }
        }
    }
}

private fun Context.granted(permission: String) = ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED

private fun Context.open(action: String) = startActivity(Intent(action, Uri.fromParts("package", packageName, null)))
