package com.ellanetworks.mcptt.call

/**
 * The PTT's presses, of each of its sources (the screen's button, a device's key): one PTT, as a radio's. The first
 * source down talks to the group ([talk], Calls.talk), the others down then joining its press; the last up releases it
 * ([release]). A press whose call has ended ([live] false) is gone: the next down talks anew.
 */
class PttController(private val talk: (group: String) -> String?, private val release: (id: String) -> Unit, private val live: (id: String) -> Boolean) {
    enum class Source { SCREEN, KEY }

    private val down = mutableSetOf<Source>()
    private var held: String? = null

    /** [source] down for [group]: the call pressed, null if refused. */
    @Synchronized
    fun press(source: Source, group: String): String? {
        val id = held?.takeIf(live) ?: talk(group)
        if (id != held) down.clear()
        held = id
        if (id != null) down += source
        return id
    }

    /** [source] up: the press released once no source is down. */
    @Synchronized
    fun release(source: Source) {
        if (!down.remove(source) || down.isNotEmpty()) return
        held?.let(release)
        held = null
    }
}
