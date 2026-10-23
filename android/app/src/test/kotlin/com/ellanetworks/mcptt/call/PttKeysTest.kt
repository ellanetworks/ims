package com.ellanetworks.mcptt.call

import android.app.Application
import android.content.Intent
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

private const val DOWN = "android.intent.action.FUNCTION_KEY_DOWN_PRESSED"
private const val UP = "android.intent.action.FUNCTION_KEY_RELEASED"

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class PttKeysTest {
    private val app = RuntimeEnvironment.getApplication()
    private val log = mutableListOf<String>()
    private var group: String? = "g"
    private val keys = PttKeys(app, PttController({ g -> "c".also { log += "talk $g" } }, { log += "release $it" }, { true })) { group }

    private fun send(action: String) {
        app.sendBroadcast(Intent(action).setPackage(app.packageName))
        shadowOf(app.mainLooper).idle()
    }

    @Test
    fun downUp() {
        keys.start()
        send(DOWN)
        send(DOWN)
        send(UP)
        assertEquals(listOf("talk g", "release c"), log)
    }

    // Stopped with the key down: released.
    @Test
    fun stoppedWhileDown() {
        keys.start()
        send(DOWN)
        keys.stop()
        assertEquals(listOf("talk g", "release c"), log)
        send(UP)
        assertEquals(listOf("talk g", "release c"), log)
    }

    // No group selected: the key does nothing.
    @Test
    fun noGroup() {
        group = null
        keys.start()
        send(DOWN)
        send(UP)
        assertEquals(emptyList<String>(), log)
    }
}
