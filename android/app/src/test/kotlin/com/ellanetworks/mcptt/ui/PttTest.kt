package com.ellanetworks.mcptt.ui

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w360dp-h220dp-xxhdpi")
class PttTest {
    @get:Rule
    val compose = createComposeRule()

    private val presses = mutableListOf<String>()

    private fun ptt(id: String? = "c1") {
        compose.setContent {
            EllaTheme(ThemeMode.LIGHT) {
                Ptt(
                    PttState.IDLE,
                    "Push to talk",
                    talkUntil = null,
                    talkFor = null,
                    onPress = {
                        presses += "press"
                        id
                    },
                    onRelease = { presses += "release $it" },
                )
            }
        }
    }

    // Held to talk, released to stop (TS 24.380 §6.2.4.4, §6.2.4.5.3).
    @Test
    fun holdTalksReleaseStops() {
        ptt()
        compose.onNodeWithContentDescription("Push to talk").performTouchInput { down(center) }
        assertEquals(listOf("press"), presses)
        compose.onNodeWithContentDescription("Push to talk").performTouchInput { up() }
        assertEquals(listOf("press", "release c1"), presses)
    }

    // A press refused (no call to talk in) has nothing to release.
    @Test
    fun refusedPressReleasesNothing() {
        ptt(id = null)
        compose.onNodeWithContentDescription("Push to talk").performTouchInput {
            down(center)
            up()
        }
        assertEquals(listOf("press"), presses)
    }

    // A finger moving off the button keeps talking until it lifts.
    @Test
    fun movingOffKeepsTalking() {
        ptt()
        compose.onNodeWithContentDescription("Push to talk").performTouchInput {
            down(center)
            moveBy(Offset(0f, height * 2f))
        }
        assertEquals(listOf("press"), presses)
        compose.onNodeWithContentDescription("Push to talk").performTouchInput { up() }
        assertEquals(listOf("press", "release c1"), presses)
    }

    // TalkBack and Switch Access cannot hold: a click starts, another stops.
    @Test
    fun accessibilityClickToggles() {
        ptt()
        val node = compose.onNodeWithContentDescription("Push to talk")
        node.performSemanticsAction(SemanticsActions.OnClick)
        node.performSemanticsAction(SemanticsActions.OnClick)
        assertEquals(listOf("press", "release c1"), presses)
    }
}
