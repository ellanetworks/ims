package com.ellanetworks.mcptt.session

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

const val SCHEMA_VERSION = 1

internal val json = Json {
    encodeDefaults = true
    ignoreUnknownKeys = true
}

@Serializable
data class Settings(
    val impi: String,
    val impu: String,
    val homeDomain: String,
    val imei: String,
    val pcscf: String,
    val network: String,
    val local: String,
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
