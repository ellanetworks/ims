package com.ellanetworks.mcptt.session

import java.io.File

/** The JSON of the session as the Go package mobile's tests keep it (mcptt/mobile/testdata/contract). */
fun contract(name: String): File = File(requireNotNull(System.getProperty("mcptt.contract")), name)
