package com.ellanetworks.mcptt.ui.theme

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Shapes
import androidx.compose.ui.unit.dp

/** The corners of Ella: Ella Core's small radii, Material's large ones. */
val shapes = Shapes(
    extraSmall = RoundedCornerShape(4.dp),
    small = RoundedCornerShape(8.dp),
    medium = RoundedCornerShape(12.dp),
    large = RoundedCornerShape(16.dp),
    extraLarge = RoundedCornerShape(28.dp),
)

/** The spacing of Ella, Ella Core's 8 dp unit, and the sizes of the field screens' targets. */
object Spacing {
    val xs = 4.dp
    val s = 8.dp
    val m = 16.dp
    val l = 24.dp
    val xl = 32.dp

    /** The padding of a screen's sides. */
    val gutter = 16.dp

    /** The least size of a target in the field screens. */
    val fieldTarget = 56.dp

    /** The least height of a field screen's primary action. */
    val primaryAction = 64.dp

    /** The least height of the PTT button. */
    val pttHeight = 160.dp
}
