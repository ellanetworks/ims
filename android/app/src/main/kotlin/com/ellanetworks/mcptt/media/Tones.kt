package com.ellanetworks.mcptt.media

import android.media.AudioManager
import android.media.ToneGenerator
import android.util.Log

private const val TAG = "EllaMcptt"
private const val VOLUME = 80

/**
 * The PTT tones (TS 22.179 §6.2.3: the talker told the floor is granted or busy, the listeners that it is free), on
 * the call's stream so that they follow its route and volume (Telecom's audio). Short system tones, as radios' chirps
 * and bonks: no vendor publishes theirs.
 */
object Tones {
    /** The floor granted: speak now. */
    fun grant() = play(ToneGenerator.TONE_PROP_BEEP, 100)

    /** The floor busy, or the PTT unavailable. */
    fun denied() = play(ToneGenerator.TONE_PROP_NACK, 250)

    /** The talker finished: the floor free. */
    fun free() = play(ToneGenerator.TONE_PROP_ACK, 120)

    /** The talk time about to end (TS 22.179 R-6.2.3.5-003). */
    fun warning() = play(ToneGenerator.TONE_PROP_PROMPT, 300)

    /** The floor taken away: revoked, or its request unanswered. */
    fun revoked() = play(ToneGenerator.TONE_PROP_BEEP2, 250)

    /** The call lost: the network ended it. */
    fun lost() = play(ToneGenerator.TONE_CDMA_CALLDROP_LITE, 600)

    private var generator: ToneGenerator? = null

    @Synchronized
    private fun play(tone: Int, ms: Int) {
        try {
            val g = generator ?: ToneGenerator(AudioManager.STREAM_VOICE_CALL, VOLUME).also { generator = it }
            g.startTone(tone, ms)
        } catch (e: RuntimeException) {
            // No tone generator of the platform now: the tone skipped, the call unaffected.
            Log.w(TAG, "tone: $e")
            generator = null
        }
    }
}
