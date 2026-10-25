package com.ellanetworks.mcptt.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontVariation
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.sp
import com.ellanetworks.mcptt.R

/** Source Code Pro, Ella's typeface: the variable font, bundled, at the weights of the scale. */
val SourceCodePro = FontFamily(
    sourceCodePro(500),
    sourceCodePro(600),
    sourceCodePro(700),
)

private fun sourceCodePro(weight: Int) = Font(
    R.font.source_code_pro,
    weight = FontWeight(weight),
    variationSettings = FontVariation.Settings(FontVariation.weight(weight)),
)

private fun style(size: TextUnit, line: TextUnit, weight: Int) = TextStyle(
    fontFamily = SourceCodePro,
    fontWeight = FontWeight(weight),
    fontSize = size,
    lineHeight = line,
    letterSpacing = 0.sp,
)

/** The type scale of Ella: Material's sizes, in Source Code Pro without letter spacing. */
val typography = Typography(
    displayLarge = style(57.sp, 64.sp, 500),
    displayMedium = style(45.sp, 52.sp, 500),
    displaySmall = style(36.sp, 44.sp, 600),
    headlineLarge = style(32.sp, 40.sp, 500),
    headlineMedium = style(28.sp, 36.sp, 500),
    headlineSmall = style(24.sp, 32.sp, 600),
    titleLarge = style(22.sp, 28.sp, 500),
    titleMedium = style(16.sp, 24.sp, 600),
    titleSmall = style(14.sp, 20.sp, 500),
    bodyLarge = style(16.sp, 24.sp, 500),
    bodyMedium = style(14.sp, 20.sp, 500),
    bodySmall = style(12.sp, 16.sp, 500),
    labelLarge = style(14.sp, 20.sp, 600),
    labelMedium = style(12.sp, 16.sp, 600),
    labelSmall = style(11.sp, 16.sp, 600),
)
