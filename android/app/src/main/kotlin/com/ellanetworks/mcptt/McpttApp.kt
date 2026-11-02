package com.ellanetworks.mcptt

import android.app.Application
import com.ellanetworks.mcptt.settings.userSettingsStore

class McpttApp : Application() {
    val settings by lazy { userSettingsStore(this) }
    val sessions by lazy { Sessions(this, settings) }
}
