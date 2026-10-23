package com.ellanetworks.mcptt.session

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Test

class SettingsTest {
    // The settings as the session reads them: mcptt/mobile/contract_test.go checks this file against its settings.
    @Test
    fun encodes() {
        val s = Settings(
            impi = "001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
            impu = "sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org",
            homeDomain = "ims.mnc001.mcc001.3gppnetwork.org",
            imei = "35000000000000",
            pcscf = "10.206.0.2:5060",
            network = "432902426637",
            local = "10.45.0.3",
            accessNetworkInfo = "3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=00101000100000101",
            mcpttId = "sip:alice@mcptt.example",
            clientId = "urn:uuid:00000000-0000-0000-0000-000000000001",
            participatingPsi = "sip:participating@mcptt.example",
            groups = listOf("sip:fire@mcptt.example"),
            logLevel = Settings.LogLevel.DEBUG,
        )

        assertEquals(
            Json.parseToJsonElement(contract("settings.json").readText()),
            Json.parseToJsonElement(s.toJson()),
        )
    }
}
