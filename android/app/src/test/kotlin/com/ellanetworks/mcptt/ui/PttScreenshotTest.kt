package com.ellanetworks.mcptt.ui

import android.os.SystemClock
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onRoot
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.Spacing
import com.ellanetworks.mcptt.ui.theme.ThemeMode
import com.github.takahirom.roborazzi.captureRoboImage
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/**
 * The PTT's every state, light and dark (src/test/screenshots/ptt): verifyRoborazziDebug compares them, recordRoborazziDebug
 * records them anew.
 */
@RunWith(ParameterizedRobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w360dp-h220dp-xxhdpi")
class PttScreenshotTest(private val state: PttState, private val label: String, private val left: Float?, private val mode: ThemeMode) {
    @get:Rule
    val compose = createComposeRule()

    @Test
    fun screenshot() {
        // The ring's frames stopped: its time left fixed.
        compose.mainClock.autoAdvance = false
        val total = 30_000L
        compose.setContent {
            EllaTheme(mode) {
                Surface(color = MaterialTheme.colorScheme.background) {
                    Ptt(
                        state,
                        label,
                        talkUntil = left?.let { SystemClock.elapsedRealtime() + (total * it).toLong() },
                        talkFor = left?.let { total },
                        onPress = { null },
                        onRelease = {},
                        modifier = Modifier.padding(Spacing.m),
                    )
                }
            }
        }
        repeat(3) { compose.mainClock.advanceTimeByFrame() }
        compose.onRoot().captureRoboImage("ptt/${state.name.lowercase()}_${mode.name.lowercase()}.png")
    }

    companion object {
        @JvmStatic
        @ParameterizedRobolectricTestRunner.Parameters(name = "{0} {3}")
        fun states(): List<Array<Any?>> = listOf(ThemeMode.LIGHT, ThemeMode.DARK).flatMap { mode ->
            listOf(
                arrayOf(PttState.IDLE, "Push to talk", null, mode),
                arrayOf(PttState.WAITING, "Connecting…", null, mode),
                arrayOf(PttState.TALKING, "Talking", 0.5f, mode),
                arrayOf(PttState.RECEIVING, "moto is talking", null, mode),
                arrayOf(PttState.BUSY, "Busy: moto is talking", null, mode),
                arrayOf(PttState.UNAVAILABLE, "Not registered", null, mode),
            )
        }
    }
}
