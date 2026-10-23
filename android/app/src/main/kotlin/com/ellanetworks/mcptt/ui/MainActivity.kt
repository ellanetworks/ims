package com.ellanetworks.mcptt.ui

import android.app.Activity
import android.app.KeyguardManager
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.LocalActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.toRoute
import com.ellanetworks.mcptt.McpttApp
import com.ellanetworks.mcptt.call.CallInfo
import com.ellanetworks.mcptt.service.SessionService
import com.ellanetworks.mcptt.settings.UserSettings
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.ThemeMode
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable

/**
 * The app's one activity. Visible, it is the while-in-use moment the service's microphone needs (decision 13); during a
 * call it shows over the lock screen, and an invitation to answer turns the screen on (its full-screen intent).
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val app = application as McpttApp
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.CREATED) {
                app.sessions.calls.current.collect { calls ->
                    setShowWhenLocked(calls.isNotEmpty())
                    setTurnScreenOn(calls.any { it.state == CallInfo.State.RINGING })
                }
            }
        }
        setContent {
            val vm = viewModel { AppViewModel(app) }
            val settings by vm.settings.collectAsStateWithLifecycle()
            EllaTheme(mode = settings?.theme.mode()) {
                App(vm)
            }
        }
    }

    override fun onStart() {
        super.onStart()
        SessionService.visible(this, true)
    }

    override fun onStop() {
        SessionService.visible(this, false)
        super.onStop()
    }
}

@Serializable
private data object HomeRoute

/** Settings, opened on its Diagnostics section, unfolded, by [diagnostics]. */
@Serializable
private data class SettingsRoute(val diagnostics: Boolean = false)

/**
 * Home the one start destination; Settings behind its button, Back returning to it. Over the lock screen, during a
 * call, Home alone: Settings, its account, sign-out and log, once the user unlocks.
 */
@Composable
private fun App(vm: AppViewModel) {
    val nav = rememberNavController()
    val activity = LocalActivity.current
    NavHost(nav, startDestination = HomeRoute) {
        composable<HomeRoute> {
            HomeScreen(
                vm,
                onSettings = { activity.unlocked { nav.navigate(SettingsRoute()) } },
                onDiagnostics = { activity.unlocked { nav.navigate(SettingsRoute(diagnostics = true)) } },
            )
        }
        composable<SettingsRoute> { entry ->
            SettingsScreen(vm, onBack = nav::popBackStack, diagnostics = entry.toRoute<SettingsRoute>().diagnostics)
        }
    }
}

/** Runs [action] unlocked: at once, or once the user dismissed the keyguard (nothing if they did not). */
private fun Activity?.unlocked(action: () -> Unit) {
    val keyguard = this?.getSystemService(KeyguardManager::class.java)
    if (keyguard == null || !keyguard.isKeyguardLocked) {
        action()
        return
    }
    keyguard.requestDismissKeyguard(
        this,
        object : KeyguardManager.KeyguardDismissCallback() {
            override fun onDismissSucceeded() = action()
        },
    )
}

private fun UserSettings.Theme?.mode(): ThemeMode = when (this) {
    UserSettings.Theme.LIGHT -> ThemeMode.LIGHT
    UserSettings.Theme.DARK -> ThemeMode.DARK
    else -> ThemeMode.SYSTEM
}
