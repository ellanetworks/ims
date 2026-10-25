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

/** A thing the session needs of the device: its explanation, and the way to grant it, none if only an admin can. */
data class Need(val text: Int, val action: (() -> Unit)?, val blocking: Boolean = false)

/**
 * What the session needs of the device now, the most important first (carrier privileges, the microphone,
 * notifications, full-screen calls, the battery, then alarms): checked again each time the app resumes.
 */
@Composable
fun rememberNeeds(vm: AppViewModel): List<Need> {
    val context = LocalContext.current
    var tick by remember { mutableIntStateOf(0) }
    LifecycleResumeEffect(Unit) {
        tick++
        onPauseOrDispose {}
    }
    val notifications = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { tick++ }
    // Allowed while the app is visible: the service takes the microphone type at once.
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
            // Granted at install outside Google Play; the user may have turned it off.
            if (!context.getSystemService(NotificationManager::class.java).canUseFullScreenIntent()) {
                add(Need(R.string.need_full_screen, { context.open(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT) }))
            }
            // Exempt from battery optimisation, the app's alarms are exact and unrestricted while the device sleeps:
            // the exact alarm permission is then not needed.
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
