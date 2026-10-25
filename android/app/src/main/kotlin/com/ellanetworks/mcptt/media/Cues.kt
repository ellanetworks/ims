package com.ellanetworks.mcptt.media

import android.content.Context
import android.os.VibrationAttributes
import android.os.VibrationEffect
import android.os.VibratorManager
import android.util.Log

private const val TAG = "EllaMcptt"

/**
 * The cues of the PTT (TS 22.179 §6.2.3), each a tone (Tones) and, where the user must feel it, a haptic: heard in
 * noise or not, felt with the phone at the mouth, the screen off, or the hardware key's press. [DeviceCues] of the
 * device, a fake's in the tests.
 */
interface Cues {
    /** The floor granted: speak now (R-6.2.3.2-004). */
    fun grant()

    /** The floor busy or denied, the PTT unavailable, or a call that failed to set up (R-6.2.3.2-002, -005). */
    fun denied()

    /** The talker finished: the floor free (R-6.2.3.4-003). */
    fun free()

    /** The talk time about to end (R-6.2.3.5-003). */
    fun warning()

    /** The floor taken away: revoked, the talk time reached (R-6.2.3.5-004), or its request unanswered. */
    fun revoked()

    /** The call lost: the network ended it. */
    fun lost()
}

/** The tones and haptics of the device. */
class DeviceCues(context: Context) : Cues {
    private val haptics = Haptics(context)

    override fun grant() {
        Tones.grant()
        haptics.vibrate(Haptics.GRANT)
    }

    override fun denied() {
        Tones.denied()
        haptics.vibrate(Haptics.REJECT)
    }

    override fun free() = Tones.free()

    override fun warning() {
        Tones.warning()
        haptics.vibrate(Haptics.WARNING)
    }

    override fun revoked() {
        Tones.revoked()
        haptics.vibrate(Haptics.LONG)
    }

    override fun lost() {
        Tones.lost()
        haptics.vibrate(Haptics.LONG)
    }
}

/**
 * The PTT's haptics, of the app rather than of a view: felt the screen off and of the hardware key. Of usage
 * COMMUNICATION_REQUEST, a request to start or end communicating with the user (VibrationAttributes), which the system
 * plays for an app in the foreground or with a foreground service, as the session's.
 */
class Haptics(context: Context) {
    private val vibrator = context.getSystemService(VibratorManager::class.java)?.defaultVibrator
    private val usage = VibrationAttributes.createForUsage(VibrationAttributes.USAGE_COMMUNICATION_REQUEST)

    fun vibrate(effect: VibrationEffect) {
        val v = vibrator?.takeIf { it.hasVibrator() } ?: return
        try {
            v.vibrate(effect, usage)
        } catch (e: RuntimeException) {
            Log.w(TAG, "haptic: $e")
        }
    }

    companion object {
        /** The talk permit: a click, as HapticFeedbackConstants.CONFIRM. */
        val GRANT: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_CLICK)

        /** Refused: a double click, as HapticFeedbackConstants.REJECT. */
        val REJECT: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_DOUBLE_CLICK)

        /** The talk time about to end. */
        val WARNING: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_HEAVY_CLICK)

        /** The floor or the call taken away: three pulses, longer than any other. */
        val LONG: VibrationEffect = VibrationEffect.createWaveform(longArrayOf(0, 120, 80, 120, 80, 240), -1)
    }
}
