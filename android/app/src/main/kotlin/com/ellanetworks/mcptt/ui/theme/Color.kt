package com.ellanetworks.mcptt.ui.theme

import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

/** The light scheme of Ella: Ella Core's tokens, the rgba ones flattened on white. */
val lightColors = lightColorScheme(
    primary = Color(0xFF26374A),
    onPrimary = Color(0xFFFFFFFF),
    primaryContainer = Color(0xFFD6E2F0),
    onPrimaryContainer = Color(0xFF26374A),
    secondary = Color(0xFF037EAA),
    onSecondary = Color(0xFFFFFFFF),
    tertiary = Color(0xFF2B3FD4),
    onTertiary = Color(0xFFFFFFFF),
    error = Color(0xFFC62828),
    onError = Color(0xFFFFFFFF),
    errorContainer = Color(0xFFFAD4D4),
    onErrorContainer = Color(0xFF7F1D1D),
    background = Color(0xFFFFFFFF),
    onBackground = Color(0xFF212121),
    surface = Color(0xFFFFFFFF),
    onSurface = Color(0xFF212121),
    surfaceVariant = Color(0xFFE2E2E2),
    onSurfaceVariant = Color(0xFF666666),
    surfaceContainerLowest = Color(0xFFFFFFFF),
    surfaceContainerLow = Color(0xFFF5F5F5),
    surfaceContainer = Color(0xFFF5F5F5),
    surfaceContainerHigh = Color(0xFFE2E2E2),
    surfaceContainerHighest = Color(0xFFE2E2E2),
    outline = Color(0xFF949494),
    outlineVariant = Color(0xFFE2E2E2),
)

/** The dark scheme of Ella: Ella Core's tokens, the rgba ones flattened on its background. */
val darkColors = darkColorScheme(
    primary = Color(0xFF5B9DFF),
    onPrimary = Color(0xFF14161B),
    primaryContainer = Color(0xFF1F3A66),
    onPrimaryContainer = Color(0xFFE3E5EA),
    secondary = Color(0xFF19AFE6),
    onSecondary = Color(0xFF14161B),
    tertiary = Color(0xFF97A3F7),
    onTertiary = Color(0xFF14161B),
    error = Color(0xFFEC9393),
    onError = Color(0xFF14161B),
    errorContainer = Color(0xFF5C1F1F),
    onErrorContainer = Color(0xFFE3E5EA),
    background = Color(0xFF14161B),
    onBackground = Color(0xFFE3E5EA),
    surface = Color(0xFF14161B),
    onSurface = Color(0xFFE3E5EA),
    surfaceVariant = Color(0xFF2E333F),
    onSurfaceVariant = Color(0xFF9D9FA4),
    surfaceContainerLowest = Color(0xFF14161B),
    surfaceContainerLow = Color(0xFF1C2027),
    surfaceContainer = Color(0xFF1C2027),
    surfaceContainerHigh = Color(0xFF2E333F),
    surfaceContainerHighest = Color(0xFF2E333F),
    outline = Color(0xFF696A6D),
    outlineVariant = Color(0xFF2E333F),
)

/**
 * The colours of Ella that Material's scheme has no role for: the states of a registration and of the floor
 * (transmit: you have the floor; success: someone else talks), and links.
 */
@Immutable
data class EllaColors(
    val success: Color,
    val onSuccess: Color,
    val successContainer: Color,
    val onSuccessContainer: Color,
    val warning: Color,
    val onWarning: Color,
    val warningContainer: Color,
    val onWarningContainer: Color,
    val info: Color,
    val onInfo: Color,
    val link: Color,
    val transmit: Color,
    val onTransmit: Color,
)

/** The extended colours of the light theme. */
val lightEllaColors = EllaColors(
    success = Color(0xFF1B6C1C),
    onSuccess = Color(0xFFFFFFFF),
    successContainer = Color(0xFFC8E6C9),
    onSuccessContainer = Color(0xFF0D3B0E),
    warning = Color(0xFFA35200),
    onWarning = Color(0xFFFFFFFF),
    warningContainer = Color(0xFFFFE0C2),
    onWarningContainer = Color(0xFF5C2B00),
    info = Color(0xFF037EAA),
    onInfo = Color(0xFFFFFFFF),
    link = Color(0xFF2B3FD4),
    transmit = Color(0xFFC62828),
    onTransmit = Color(0xFFFFFFFF),
)

/** The extended colours of the dark theme. */
val darkEllaColors = EllaColors(
    success = Color(0xFF4ABF4B),
    onSuccess = Color(0xFF14161B),
    successContainer = Color(0xFF1E4620),
    onSuccessContainer = Color(0xFFE3E5EA),
    warning = Color(0xFFF09142),
    onWarning = Color(0xFF14161B),
    warningContainer = Color(0xFF5A3410),
    onWarningContainer = Color(0xFFE3E5EA),
    info = Color(0xFF19AFE6),
    onInfo = Color(0xFF14161B),
    link = Color(0xFF97A3F7),
    transmit = Color(0xFFEC9393),
    onTransmit = Color(0xFF14161B),
)

/** The extended colours of the theme, EllaTheme's. */
val LocalEllaColors = staticCompositionLocalOf { lightEllaColors }
