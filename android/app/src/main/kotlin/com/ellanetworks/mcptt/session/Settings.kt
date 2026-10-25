package com.ellanetworks.mcptt.session

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/** The schema version of the settings and events, the Go package mobile's schemaVersion. */
const val SCHEMA_VERSION = 1

internal val json = Json {
    encodeDefaults = true
    ignoreUnknownKeys = true
}

/**
 * The settings of a session (the Go package mobile's doc): the ISIM's identities, the device's, the network's, and
 * the user's MCPTT settings until the CMS (decision 6).
 */
@Serializable
data class Settings(
    val impi: String,
    val impu: String,
    val homeDomain: String,
    val imei: String,
    /** The P-CSCF, addr:port. */
    val pcscf: String,
    /** The platform's handle of the MCPTT network (Network.getNetworkHandle), and the device's address on it. */
    val network: String,
    val local: String,
    /** The P-Access-Network-Info of the cell (TS 24.229 §7.2A.4). */
    val accessNetworkInfo: String,
    val mcpttId: String,
    val clientId: String,
    val participatingPsi: String,
    val groups: List<String>,
    val logLevel: LogLevel = LogLevel.INFO,
    val version: Int = SCHEMA_VERSION,
) {
    @Serializable
    enum class LogLevel {
        @SerialName("debug")
        DEBUG,

        @SerialName("info")
        INFO,

        @SerialName("warn")
        WARN,

        @SerialName("error")
        ERROR,
    }

    fun toJson(): String = json.encodeToString(this)
}
