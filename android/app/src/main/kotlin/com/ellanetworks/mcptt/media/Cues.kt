package com.ellanetworks.mcptt.media

import android.content.Context
import android.os.VibrationAttributes
import android.os.VibrationEffect
import android.os.VibratorManager
import android.util.Log

private const val TAG = "EllaMcptt"

// TS 22.179 clause 6.2.3
interface Cues {
    fun grant()

    fun denied()

    fun free()

    fun warning()

    fun revoked()

    fun lost()
}

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
        val GRANT: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_CLICK)

        val REJECT: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_DOUBLE_CLICK)

        val WARNING: VibrationEffect = VibrationEffect.createPredefined(VibrationEffect.EFFECT_HEAVY_CLICK)

        val LONG: VibrationEffect = VibrationEffect.createWaveform(longArrayOf(0, 120, 80, 120, 80, 240), -1)
    }
}
