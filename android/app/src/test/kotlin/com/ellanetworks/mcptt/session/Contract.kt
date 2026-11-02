package com.ellanetworks.mcptt.session

import java.io.File

fun contract(name: String): File = File(requireNotNull(System.getProperty("mcptt.contract")), name)
