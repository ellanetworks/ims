package com.ellanetworks.mcptt.settings

import android.content.Context
import androidx.datastore.core.CorruptionException
import androidx.datastore.core.DataStore
import androidx.datastore.core.DataStoreFactory
import androidx.datastore.core.Serializer
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.File
import java.io.InputStream
import java.io.OutputStream
import java.util.UUID

@Serializable
data class UserSettings(
    val pcscf: String = "",
    val mcpttId: String = "",
    val participatingPsi: String = "",
    val groups: List<String> = emptyList(),
    val clientId: String = "",
    val startOnBoot: Boolean = true,
    val selectedGroup: String = "",
    val theme: Theme = Theme.SYSTEM,
    val tones: Boolean = true,
) {
    enum class Theme { SYSTEM, LIGHT, DARK }

    companion object {
        const val MCPTT_ID = "MCPTT ID"
    }

    val group: String?
        get() = selectedGroup.takeIf { it in groups } ?: groups.firstOrNull()

    fun session(): UserSettings = UserSettings(pcscf, mcpttId, participatingPsi, groups, clientId, startOnBoot)

    val missing: List<String>
        get() = buildList {
            if (mcpttId.isBlank()) add(MCPTT_ID)
        }
}

private val SCHEME = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")

// TS 23.280 clause 8.1.2
// TS 23.280 clause 8.1.3.1
fun sipUri(text: String): String = text.trim().let { if (it.isEmpty() || SCHEME.containsMatchIn(it)) it else "sip:$it" }

fun groupUri(text: String, mcpttId: String): String {
    val t = text.trim()
    val domain = domainOf(mcpttId)
    return if (t.isNotEmpty() && '@' !in t && !SCHEME.containsMatchIn(t) && domain.isNotEmpty()) "sip:$t@$domain" else sipUri(t)
}

fun domainOf(uri: String): String = uri.substringAfter('@', "").substringBefore(';').substringBefore('>')

fun shown(uri: String): String = uri.removePrefix("sip:")

private val CLIENT_ID = Regex(""""clientId"\s*:\s*"(urn:uuid:[0-9A-Fa-f-]{36})"""")

private class Unreadable(val clientId: String, cause: Throwable) : Exception(cause)

private object UserSettingsSerializer : Serializer<UserSettings> {
    private val json = Json {
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    override val defaultValue = UserSettings()

    override suspend fun readFrom(input: InputStream): UserSettings {
        val text = input.readBytes().decodeToString()
        return try {
            json.decodeFromString(text)
        } catch (e: IllegalArgumentException) {
            val id = CLIENT_ID.find(text)?.groupValues?.get(1).orEmpty()
            throw CorruptionException("settings", Unreadable(id, e))
        }
    }

    override suspend fun writeTo(t: UserSettings, output: OutputStream) = output.write(json.encodeToString(t).encodeToByteArray())
}

fun userSettingsStore(context: Context): DataStore<UserSettings> = userSettingsStore(File(context.filesDir, "datastore/settings.json"))

internal fun userSettingsStore(file: File): DataStore<UserSettings> = DataStoreFactory.create(
    serializer = UserSettingsSerializer,
    produceFile = { file },
    corruptionHandler = ReplaceFileCorruptionHandler { e ->
        val unreadable = generateSequence<Throwable>(e) { it.cause }.filterIsInstance<Unreadable>().firstOrNull()
        UserSettings(clientId = unreadable?.clientId.orEmpty())
    },
)

// RFC 9562
suspend fun DataStore<UserSettings>.withClientId(): UserSettings = updateData {
    if (it.clientId.isNotEmpty()) it else it.copy(clientId = "urn:uuid:${UUID.randomUUID()}")
}
