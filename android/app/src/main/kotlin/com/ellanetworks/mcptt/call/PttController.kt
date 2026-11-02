package com.ellanetworks.mcptt.call

class PttController(private val talk: (group: String) -> String?, private val release: (id: String) -> Unit, private val live: (id: String) -> Boolean) {
    enum class Source { SCREEN, KEY }

    private val down = mutableSetOf<Source>()
    private var held: String? = null

    @Synchronized
    fun press(source: Source, group: String): String? {
        val id = held?.takeIf(live) ?: talk(group)
        if (id != held) down.clear()
        held = id
        if (id != null) down += source
        return id
    }

    @Synchronized
    fun release(source: Source) {
        if (!down.remove(source) || down.isNotEmpty()) return
        held?.let(release)
        held = null
    }
}
