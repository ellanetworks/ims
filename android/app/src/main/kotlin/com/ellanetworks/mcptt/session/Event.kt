package com.ellanetworks.mcptt.session

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds

sealed interface Event {
    data class Registered(val impu: String) : Event

    data class RegistrationFailed(val error: String) : Event

    // TS 24.229 clause 5.1.1.7
    data class Deregistered(val reason: String, val retryAfter: Duration) : Event

    data class Stopped(val reason: StopReason) : Event

    data class Affiliation(val status: Int, val error: String?) : Event

    sealed interface OfCall : Event {
        val call: String
    }

    data class Incoming(override val call: String, val group: String, val caller: String, val manual: Boolean) : OfCall

    data class Established(override val call: String) : OfCall

    data class Ended(override val call: String, val cause: Cause, val status: Int, val error: String?) : OfCall

    // TS 24.380 clause 6.2.4
    // TS 24.380 clause 8.2.3.3
    data class Floor(override val call: String, val notice: Notice, val party: String?, val duration: Int? = null) : OfCall

    data class Transmit(override val call: String, val on: Boolean, val mode: Int?) : OfCall

    // RFC 4867 section 4.3.1
    data class ModeRequest(override val call: String, val mode: Int) : OfCall

    data class Heard(override val call: String, val party: String) : OfCall

    @Serializable
    enum class StopReason {
        @SerialName("stop")
        STOP,

        @SerialName("rejected")
        REJECTED,
    }

    @Serializable
    enum class Cause {
        @SerialName("local")
        LOCAL,

        @SerialName("remote")
        REMOTE,

        @SerialName("rejected")
        REJECTED,

        @SerialName("missed")
        MISSED,

        @SerialName("error")
        ERROR,
    }

    @Serializable
    enum class Notice {
        @SerialName("granted")
        GRANTED,

        @SerialName("denied")
        DENIED,

        @SerialName("taken")
        TAKEN,

        @SerialName("idle")
        IDLE,

        @SerialName("revoked")
        REVOKED,

        @SerialName("timeout")
        TIMEOUT,

        @SerialName("media-ended")
        MEDIA_ENDED,
    }

    companion object {
        fun decode(text: String): Event {
            val w = json.decodeFromString<Wire>(text)
            if (w.version != SCHEMA_VERSION) {
                throw SerializationException("event of schema ${w.version}, want $SCHEMA_VERSION")
            }

            fun <T> need(v: T?, name: String): T = v ?: throw SerializationException("${w.type} event without $name")
            val call by lazy { need(w.call, "call") }

            return when (w.type) {
                "registration" -> if (w.registered) Registered(need(w.impu, "impu")) else RegistrationFailed(need(w.error, "error"))

                "deregistered" -> Deregistered(need(w.reason, "reason"), w.retryAfter.seconds)

                "stopped" -> Stopped(enumOf(need(w.reason, "reason")))

                "affiliation" -> Affiliation(w.status, w.error)

                "call" -> when (w.kind) {
                    "incoming" -> Incoming(call, need(w.group, "group"), need(w.caller, "caller"), w.manual)
                    "established" -> Established(call)
                    "ended" -> Ended(call, enumOf(need(w.cause, "cause")), w.status, w.error)
                    else -> throw SerializationException("call event of kind ${w.kind}")
                }

                "floor" -> Floor(call, enumOf(need(w.notice, "notice")), w.party, w.duration)

                "transmit" -> need(w.on, "on").let { Transmit(call, it, if (it) need(w.mode, "mode") else null) }

                "mode" -> ModeRequest(call, need(w.mode, "mode"))

                "heard" -> Heard(call, need(w.party, "party"))

                else -> throw SerializationException("event of type ${w.type}")
            }
        }

        private inline fun <reified T : Enum<T>> enumOf(name: String): T = json.decodeFromString<T>("\"$name\"")
    }
}

@Serializable
private data class Wire(
    val version: Int,
    val type: String,
    val call: String? = null,
    val registered: Boolean = false,
    val impu: String? = null,
    val reason: String? = null,
    val retryAfter: Long = 0,
    val kind: String? = null,
    val group: String? = null,
    val caller: String? = null,
    val manual: Boolean = false,
    val cause: String? = null,
    val notice: String? = null,
    val party: String? = null,
    val duration: Int? = null,
    val on: Boolean? = null,
    val mode: Int? = null,
    val status: Int = 0,
    val error: String? = null,
)
