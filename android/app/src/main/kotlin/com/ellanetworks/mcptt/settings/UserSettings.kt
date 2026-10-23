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

/**
 * The user's MCPTT settings, in the app until the CMS and IdMS (decision 6): the P-CSCF, empty for the ISIM's; the
 * MCPTT ID; the participating function's PSI, empty for that of the MCPTT AS of Ella IMS of the ISIM's home domain; the
 * groups; and the MCPTT client ID, generated when the client is first used and kept until a factory reset (TS 24.379
 * §4.10).
 */
@Serializable
data class UserSettings(
    val pcscf: String = "",
    val mcpttId: String = "",
    val participatingPsi: String = "",
    val groups: List<String> = emptyList(),
    val clientId: String = "",
    /** Start the service when the device boots. */
    val startOnBoot: Boolean = true,
    /** The group the PTT talks to, a radio's channel: one of [groups], the first if none. */
    val selectedGroup: String = "",
    val theme: Theme = Theme.SYSTEM,
    /** The floor's tones beyond the grant and the busy one (Calls.tones). */
    val tones: Boolean = true,
) {
    enum class Theme { SYSTEM, LIGHT, DARK }

    companion object {
        const val MCPTT_ID = "MCPTT ID"
    }

    /** The selected group of the user's: the one chosen, else the first. */
    val group: String?
        get() = selectedGroup.takeIf { it in groups } ?: groups.firstOrNull()

    /** The settings the session is built of: what the user sees and hears of it alone left out (Sessions). */
    fun session(): UserSettings = UserSettings(pcscf, mcpttId, participatingPsi, groups, clientId, startOnBoot)

    /** What the user must still set before the session starts, or empty. */
    val missing: List<String>
        get() = buildList {
            if (mcpttId.isBlank()) add(MCPTT_ID)
        }
}

private val SCHEME = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")

/**
 * The URI of an MCPTT ID or group ID the user typed (TS 23.280 §8.1.2, §8.1.3.1: a URI, of any domain): with no
 * scheme, SIP's, IMS's.
 */
fun sipUri(text: String): String = text.trim().let { if (it.isEmpty() || SCHEME.containsMatchIn(it)) it else "sip:$it" }

/** The URI of a group the user typed: a name alone, of the user's MC system, [mcpttId]'s domain. */
fun groupUri(text: String, mcpttId: String): String {
    val t = text.trim()
    val domain = domainOf(mcpttId)
    return if (t.isNotEmpty() && '@' !in t && !SCHEME.containsMatchIn(t) && domain.isNotEmpty()) "sip:$t@$domain" else sipUri(t)
}

/** The domain of a SIP URI, its MC system's; empty if none. */
fun domainOf(uri: String): String = uri.substringAfter('@', "").substringBefore(';').substringBefore('>')

/** A SIP URI as the user types it: without its scheme. */
fun shown(uri: String): String = uri.removePrefix("sip:")

/** The client ID of a settings file that is not valid JSON of UserSettings, if it can be read still. */
private val CLIENT_ID = Regex(""""clientId"\s*:\s*"(urn:uuid:[0-9A-Fa-f-]{36})"""")

/** A settings file not read: its client ID, if found in it. */
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
            // SerializationException's, and the decoder's of a value out of its type.
            val id = CLIENT_ID.find(text)?.groupValues?.get(1).orEmpty()
            throw CorruptionException("settings", Unreadable(id, e))
        }
    }

    override suspend fun writeTo(t: UserSettings, output: OutputStream) = output.write(json.encodeToString(t).encodeToByteArray())
}

/** The store of the user's settings, a file of the app's: excluded from backups (data_extraction_rules.xml). */
fun userSettingsStore(context: Context): DataStore<UserSettings> = userSettingsStore(File(context.filesDir, "datastore/settings.json"))

/**
 * The store of the settings in [file]. A file corrupt is replaced by the defaults, not thrown at each read (the
 * service's crash at each start, and at boot): the user's settings lost, typed again. Its client ID is kept if found
 * in it; else a new one is generated (withClientId), the participating function seeing another client of the user's
 * (TS 24.379 §4.10: kept until a factory reset, but a file of no ID has none to keep).
 */
internal fun userSettingsStore(file: File): DataStore<UserSettings> = DataStoreFactory.create(
    serializer = UserSettingsSerializer,
    produceFile = { file },
    corruptionHandler = ReplaceFileCorruptionHandler { e ->
        // The serializer's, of DataStore's own of the file (its diagnostics).
        val unreadable = generateSequence<Throwable>(e) { it.cause }.filterIsInstance<Unreadable>().firstOrNull()
        UserSettings(clientId = unreadable?.clientId.orEmpty())
    },
)

/** The settings with a client ID, generated once: a URN of a random UUID (RFC 9562, version 4). */
suspend fun DataStore<UserSettings>.withClientId(): UserSettings = updateData {
    if (it.clientId.isNotEmpty()) it else it.copy(clientId = "urn:uuid:${UUID.randomUUID()}")
}
