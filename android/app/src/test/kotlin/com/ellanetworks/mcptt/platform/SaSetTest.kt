package com.ellanetworks.mcptt.platform

import com.ellanetworks.mcptt.session.contract
import org.junit.Assert.assertEquals
import org.junit.Test

class SaSetTest {
    // The SA set as the session encodes it: mobile's TestSAJSON keeps it.
    @Test
    fun decodes() {
        val s = SaSet.decode(contract("saset.json").readText())
        assertEquals(
            SaSet(
                id = "1-2",
                local = SaEndpoint("10.45.0.3", 40000, 40001, 1, 2),
                remote = SaEndpoint("10.206.0.2", 5064, 5063, 3, 4),
                integrity = "hmac-sha-1-96",
                encryption = "aes-cbc",
                ik = "ab".repeat(16) + "00000000",
                ck = "cd".repeat(16),
            ),
            s,
        )
    }
}
