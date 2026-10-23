package com.ellanetworks.mcptt

import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.delay
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withContext
import org.junit.Assert.assertEquals
import org.junit.Test

class RunsTest {
    /** A run's watcher, registered once at most (ConnectivityManager's NetworkCallback), its teardown slow. */
    private class Watcher {
        var registered = false
        val log = mutableListOf<String>()

        suspend fun run(n: Int) {
            check(!registered) { "already registered" }
            registered = true
            log += "start $n"
            try {
                awaitCancellation()
            } finally {
                withContext(NonCancellable) {
                    delay(5_000) // The deregistration.
                    registered = false
                    log += "stop $n"
                }
            }
        }
    }

    // The runs are of backgroundScope: past every teardown (advanceUntilIdle leaves background work).
    private fun TestScope.settle() {
        advanceTimeBy(60_000)
        runCurrent()
    }

    private fun TestScope.runs(w: Watcher): Runs {
        var n = 0
        return Runs(backgroundScope) { w.run(++n) }
    }

    @Test
    fun startAfterStopWaitsForTeardown() = runTest(StandardTestDispatcher()) {
        val w = Watcher()
        val r = runs(w)
        r.start()
        settle()
        r.stop()
        r.start()
        settle()
        assertEquals(listOf("start 1", "stop 1", "start 2"), w.log)
        assertEquals(true, w.registered)
    }

    @Test
    fun startWhileRunningIsNoOp() = runTest(StandardTestDispatcher()) {
        val w = Watcher()
        val r = runs(w)
        r.start()
        r.start()
        settle()
        assertEquals(listOf("start 1"), w.log)
    }

    @Test
    fun stopBeforeTheWaitingStartRuns() = runTest(StandardTestDispatcher()) {
        val w = Watcher()
        val r = runs(w)
        r.start()
        settle()
        r.stop()
        r.start() // Waits for the teardown, then cancelled before running.
        r.stop()
        r.start()
        settle()
        assertEquals(listOf("start 1", "stop 1", "start 2"), w.log)
    }
}
