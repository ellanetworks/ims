package com.ellanetworks.mcptt.session

import kotlinx.serialization.SerializationException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import kotlin.time.Duration.Companion.seconds

class EventTest {
    // The events as the session encodes them: mcptt/mobile/contract_test.go's, in its order.
    @Test
    fun decodes() {
        val want = listOf(
            Event.Registered("sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org"),
            Event.RegistrationFailed("mobile: register: timeout"),
            Event.Deregistered("probation", 30.seconds),
            Event.Deregistered("unregistered", 0.seconds),
            Event.Stopped(Event.StopReason.REJECTED),
            Event.Stopped(Event.StopReason.STOP),
            Event.Affiliation(200, null),
            Event.Affiliation(0, "mobile: affiliation: 403"),
            Event.Incoming("in-1", "sip:fire@mcptt.example", "sip:alice@mcptt.example", true),
            Event.Established("c1"),
            Event.Ended("c1", Event.Cause.REMOTE, 0, null),
            Event.Ended("c2", Event.Cause.REJECTED, 480, "client: INVITE rejected"),
            Event.Ended("c3", Event.Cause.LOCAL, 0, null),
            Event.Ended("in-1", Event.Cause.MISSED, 0, null),
            Event.Ended("c4", Event.Cause.ERROR, 0, "client: INVITE: timeout"),
            Event.Floor("c1", Event.Notice.TAKEN, "sip:bob@mcptt.example"),
            Event.Floor("c1", Event.Notice.GRANTED, null, duration = 30),
            Event.Floor("c1", Event.Notice.DENIED, null),
            Event.Floor("c1", Event.Notice.IDLE, null),
            Event.Floor("c1", Event.Notice.REVOKED, null),
            Event.Floor("c1", Event.Notice.TIMEOUT, null),
            Event.Floor("c1", Event.Notice.MEDIA_ENDED, null),
            Event.Transmit("c1", true, 8),
            Event.Transmit("c1", false, null),
            Event.ModeRequest("c1", 0),
            Event.Heard("c1", "sip:bob@mcptt.example"),
        )

        assertEquals(want, events())
    }

    // Every notice and cause the app knows is one the session sends: a rename on either side fails here, not as an
    // event dropped (mcptt/mobile/contract_test.go's TestContractExhaustive, the other side).
    @Test
    fun exhaustive() {
        val events = events()
        assertEquals(Event.Notice.entries.toSet(), events.filterIsInstance<Event.Floor>().map { it.notice }.toSet())
        assertEquals(Event.Cause.entries.toSet(), events.filterIsInstance<Event.Ended>().map { it.cause }.toSet())
    }

    private fun events() = contract("events.jsonl").readLines().filter(String::isNotEmpty).map(Event::decode)

    @Test
    fun rejects() {
        for (text in listOf(
            """{"version":2,"type":"heard","call":"c","party":"p"}""",
            """{"version":1,"type":"heard","party":"p"}""",
            """{"version":1,"type":"later","call":"c"}""",
            """{"version":1,"type":"call","call":"c","kind":"ended","cause":"unknown"}""",
            """{"version":1,"type":"call","call":"c","kind":"ringing"}""",
            """{"version":1,"type":"transmit","call":"c","on":true}""",
            """not json""",
        )) {
            assertThrows(text, SerializationException::class.java) { Event.decode(text) }
        }
    }
}
