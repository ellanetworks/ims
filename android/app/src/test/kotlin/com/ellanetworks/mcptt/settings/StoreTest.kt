package com.ellanetworks.mcptt.settings

import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class StoreTest {
    @get:Rule
    val dir = TemporaryFolder()

    private fun read(text: String): UserSettings {
        val f = dir.newFile()
        f.writeText(text)
        return runBlocking { userSettingsStore(f).data.first() }
    }

    @Test
    fun valid() {
        assertEquals("sip:a@b", read("""{"mcpttId":"sip:a@b"}""").mcpttId)
    }

    @Test
    fun corruptReplacedKeepingClientId() {
        val id = "urn:uuid:0f8fad5b-d9cb-469f-a165-70867728950e"
        assertEquals(UserSettings(clientId = id), read("""{"mcpttId":"sip:a@b","clientId":"$id","groups":[tru"""))
    }

    @Test
    fun corruptReplacedByDefaults() {
        assertEquals(UserSettings(), read("\u0000\u0001garbage"))
    }
}
