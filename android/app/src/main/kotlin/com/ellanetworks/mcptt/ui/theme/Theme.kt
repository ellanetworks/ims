package com.ellanetworks.mcptt.ui.theme

import android.app.Activity
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.SideEffect
import androidx.compose.ui.platform.LocalView
import androidx.core.view.WindowCompat

/** The theme the app shows: the system's, or light or dark whatever the system's. */
enum class ThemeMode { SYSTEM, LIGHT, DARK }

/**
 * The Ella brand theme, light or dark by [mode]: Material's scheme, type and shapes, the extended colours, and the
 * system bars' icons in contrast with it. No dynamic colour: the fleet's phones look alike.
 */
@Composable
fun EllaTheme(mode: ThemeMode = ThemeMode.SYSTEM, content: @Composable () -> Unit) {
    val dark = when (mode) {
        ThemeMode.SYSTEM -> isSystemInDarkTheme()
        ThemeMode.LIGHT -> false
        ThemeMode.DARK -> true
    }
    val view = LocalView.current
    if (!view.isInEditMode) {
        SideEffect {
            val window = (view.context as Activity).window
            WindowCompat.getInsetsController(window, view).apply {
                isAppearanceLightStatusBars = !dark
                isAppearanceLightNavigationBars = !dark
            }
        }
    }
    CompositionLocalProvider(LocalEllaColors provides if (dark) darkEllaColors else lightEllaColors) {
        MaterialTheme(
            colorScheme = if (dark) darkColors else lightColors,
            typography = typography,
            shapes = shapes,
            content = content,
        )
    }
}

/** The theme's values beyond MaterialTheme's. */
object EllaTheme {
    /** The extended colours of the theme. */
    val colors: EllaColors
        @Composable
        @ReadOnlyComposable
        get() = LocalEllaColors.current
}
