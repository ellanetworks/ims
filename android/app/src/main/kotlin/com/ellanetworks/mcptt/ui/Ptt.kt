package com.ellanetworks.mcptt.ui

import android.os.SystemClock
import android.view.HapticFeedbackConstants
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Block
import androidx.compose.material.icons.rounded.Mic
import androidx.compose.material.icons.rounded.MicOff
import androidx.compose.material.icons.rounded.VolumeUp
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.FloatState
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameMillis
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Outline
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathMeasure
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.ellanetworks.mcptt.R
import com.ellanetworks.mcptt.ui.theme.EllaTheme
import com.ellanetworks.mcptt.ui.theme.Spacing

/** The floor's state as the PTT shows it, each its colour, icon and word (the brief's floor-state colours). */
enum class PttState {
    /** The PTT cannot talk now: not registered, the call held. */
    UNAVAILABLE,
    IDLE,

    /** Pressed, the floor requested or the call placed. */
    WAITING,
    TALKING,

    /** Another talks: still pressable, the request then refused or queued (TS 22.179 R-6.2.3.2-005). */
    RECEIVING,

    /** The request just refused. */
    BUSY,
}

private val RING = 6.dp

/** The ring's gap to the button's edge: wholly on the fill, its colour the content's, whatever the page behind. */
private val RING_GAP = 6.dp
private const val TRACK_ALPHA = 0.25f

private fun Float?.orZero() = this ?: 0f

/**
 * The PTT of the selected group, and the floor's state on it: the one element to look at. Held to talk, released to
 * stop (TS 24.380 §6.2.4.4, §6.2.4.5.3). While the user talks, a ring
 * along its border shrinks with the talk time left ([talkUntil] of [talkFor], the grant's Duration, TS 22.179
 * R-6.2.3.5-003). Held until the last finger lifts, wherever it moves: its moves consumed, a scrolling screen never takes
 * it. [onPress] returns the call pressed, null if refused; [onRelease] releases it. TalkBack and Switch Access start and
 * stop it with actions, holding being out of their reach.
 */
@Composable
fun Ptt(state: PttState, label: String, talkUntil: Long?, talkFor: Long?, onPress: () -> String?, onRelease: (String) -> Unit, modifier: Modifier = Modifier) {
    val view = LocalView.current
    val press by rememberUpdatedState(onPress)
    val release by rememberUpdatedState(onRelease)
    var held by remember { mutableStateOf<String?>(null) }

    // The press a light tick, its key's; the grant's and the refusal's haptics are Calls' (Cues), the floor's answer.
    fun start(): Boolean {
        val id = press()
        view.performHapticFeedback(HapticFeedbackConstants.VIRTUAL_KEY)
        held = id
        return id != null
    }

    fun stop() {
        held?.let(release)
        held = null
    }

    val colors = EllaTheme.colors
    val scheme = MaterialTheme.colorScheme
    val (fill, content, icon) = when (state) {
        PttState.UNAVAILABLE -> Triple(scheme.surfaceContainerHighest, scheme.onSurfaceVariant, Icons.Rounded.MicOff)
        PttState.IDLE, PttState.WAITING -> Triple(scheme.primary, scheme.onPrimary, Icons.Rounded.Mic)
        PttState.TALKING -> Triple(colors.transmit, colors.onTransmit, Icons.Rounded.Mic)
        PttState.RECEIVING -> Triple(colors.success, colors.onSuccess, Icons.Rounded.VolumeUp)
        PttState.BUSY -> Triple(colors.warning, colors.onWarning, Icons.Rounded.Block)
    }
    val description = stringResource(
        when (state) {
            PttState.TALKING -> R.string.ptt_state_talking
            PttState.WAITING -> R.string.ptt_state_waiting
            else -> R.string.ptt_state_idle
        },
    )
    val startLabel = stringResource(R.string.ptt_start)
    val stopLabel = stringResource(R.string.ptt_stop)
    val left = talkLeft(state == PttState.TALKING, talkUntil, talkFor)
    val shape = MaterialTheme.shapes.extraLarge

    Box(
        modifier
            .fillMaxWidth()
            .height(Spacing.pttHeight)
            .background(fill, shape)
            .then(if (state == PttState.UNAVAILABLE) Modifier.border(BorderStroke(1.dp, scheme.outline), shape) else Modifier)
            .drawWithContent {
                drawContent()
                // Read here, in the draw phase: each frame redraws the ring, not the button.
                val left = left?.floatValue
                if (left != null) {
                    val inset = RING_GAP.toPx() + RING.toPx() / 2
                    val outline = shape.createOutline(size, layoutDirection, this)
                    val radius = ((outline as? Outline.Rounded)?.roundRect?.topLeftCornerRadius?.x.orZero() - inset)
                        .coerceIn(0f, minOf(size.width, size.height) / 2 - inset)
                    val path = border(Rect(inset, inset, size.width - inset, size.height - inset), radius)
                    val measure = PathMeasure().apply { setPath(path, false) }
                    drawPath(path, content.copy(alpha = TRACK_ALPHA), style = Stroke(width = RING.toPx()))
                    val segment = Path()
                    measure.getSegment(0f, measure.length * left, segment, true)
                    drawPath(segment, content, style = Stroke(width = RING.toPx(), cap = StrokeCap.Round))
                }
            }
            .semantics {
                role = Role.Button
                contentDescription = label
                stateDescription = description
                // Told what others do, the floor's changes; not the user's own talking, which they know.
                if (state != PttState.TALKING && state != PttState.WAITING) liveRegion = LiveRegionMode.Polite
                onClick(if (held == null) startLabel else stopLabel) {
                    if (held == null) start() else stop()
                    true
                }
                customActions = listOf(
                    CustomAccessibilityAction(startLabel) { start() },
                    CustomAccessibilityAction(stopLabel) {
                        stop()
                        true
                    },
                )
            }
            .pointerInput(Unit) {
                awaitEachGesture {
                    awaitFirstDown().consume()
                    start()
                    try {
                        do {
                            val event = awaitPointerEvent()
                            event.changes.forEach { it.consume() }
                        } while (event.changes.any { it.pressed })
                    } finally {
                        // Lifted, or the gesture cancelled: released.
                        stop()
                    }
                }
            },
        contentAlignment = Alignment.Center,
    ) {
        Content(icon, label, content)
    }
}

@Composable
private fun Content(icon: ImageVector, label: String, color: Color) {
    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(Spacing.s)) {
        Icon(icon, contentDescription = null, tint = color, modifier = Modifier.size(40.dp))
        Text(
            label,
            color = color,
            style = MaterialTheme.typography.headlineSmall,
            textAlign = TextAlign.Center,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(horizontal = Spacing.m),
        )
    }
}

/**
 * A rounded rectangle's border from its top's middle, clockwise: the ring's start, as a clock's. A platform's rounded
 * rectangle starts elsewhere, its corner, and differs between versions.
 */
private fun border(r: Rect, radius: Float) = Path().apply {
    val d = 2 * radius
    moveTo(r.center.x, r.top)
    lineTo(r.right - radius, r.top)
    arcTo(Rect(r.right - d, r.top, r.right, r.top + d), -90f, 90f, false)
    lineTo(r.right, r.bottom - radius)
    arcTo(Rect(r.right - d, r.bottom - d, r.right, r.bottom), 0f, 90f, false)
    lineTo(r.left + radius, r.bottom)
    arcTo(Rect(r.left, r.bottom - d, r.left + d, r.bottom), 90f, 90f, false)
    lineTo(r.left, r.top + radius)
    arcTo(Rect(r.left, r.top, r.left + d, r.top + d), 180f, 90f, false)
    close()
}

/** The fraction of the talk time left, each frame; null when the user does not talk or no time was granted. */
@Composable
private fun talkLeft(talking: Boolean, until: Long?, total: Long?): FloatState? {
    val shown = talking && until != null && total != null && total > 0
    val left = remember(until) { mutableFloatStateOf(1f) }
    LaunchedEffect(shown, until, total) {
        if (!shown) return@LaunchedEffect
        while (true) {
            withFrameMillis { }
            left.floatValue = ((until!! - SystemClock.elapsedRealtime()).toFloat() / total!!).coerceIn(0f, 1f)
            if (left.floatValue == 0f) break
        }
    }
    return left.takeIf { shown }
}
