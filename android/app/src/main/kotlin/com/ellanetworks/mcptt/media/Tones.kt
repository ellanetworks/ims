package com.ellanetworks.mcptt.media

import android.media.AudioManager
import android.media.ToneGenerator
import android.util.Log

private const val TAG = "EllaMcptt"
private const val VOLUME = 80

// TS 22.179 clause 6.2.3
object Tones {
    fun grant() = play(ToneGenerator.TONE_PROP_BEEP, 100)

    fun denied() = play(ToneGenerator.TONE_PROP_NACK, 250)

    fun free() = play(ToneGenerator.TONE_PROP_ACK, 120)

    fun warning() = play(ToneGenerator.TONE_PROP_PROMPT, 300)

    fun revoked() = play(ToneGenerator.TONE_PROP_BEEP2, 250)

    fun lost() = play(ToneGenerator.TONE_CDMA_CALLDROP_LITE, 600)

    private var generator: ToneGenerator? = null

    @Synchronized
    private fun play(tone: Int, ms: Int) {
        try {
            val g = generator ?: ToneGenerator(AudioManager.STREAM_VOICE_CALL, VOLUME).also { generator = it }
            g.startTone(tone, ms)
        } catch (e: RuntimeException) {
            Log.w(TAG, "tone: $e")
            generator = null
        }
    }
}
