package de.voilde.nodepanel.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

// Warm-gray design tokens ported from web/src/styles/themes.scss. Material
// ColorScheme covers the defaults; NpColors carries the web-specific tokens
// (borders, three-tier text, badge backgrounds) that have no ColorScheme slot.

@Immutable
data class NpColors(
    val bgPage: Color,
    val bgCard: Color,
    val bgTertiary: Color,
    val bgHover: Color,
    val bgInput: Color,
    val textPrimary: Color,
    val textSecondary: Color,
    val textTertiary: Color,
    val border: Color,
    val borderStrong: Color,
    val primary: Color,
    val primaryHover: Color,
    val success: Color,
    val successBg: Color,
    val warning: Color,
    val warningBg: Color,
    val amber: Color,
    val amberBg: Color,
)

val NpLightColors = NpColors(
    bgPage = Color(0xFFFAF9F5),
    bgCard = Color(0xFFF0EEE8),
    bgTertiary = Color(0xFFE9E6DF),
    bgHover = Color(0xFFE9E6DF),
    bgInput = Color(0xFFFFFFFF),
    textPrimary = Color(0xFF2D2A26),
    textSecondary = Color(0xFF6D6760),
    textTertiary = Color(0xFFA29C95),
    border = Color(0xFFE3E1DB),
    borderStrong = Color(0xFFD5D2CB),
    primary = Color(0xFF8B8680),
    primaryHover = Color(0xFF7F7A74),
    success = Color(0xFF10B981),
    successBg = Color(0xFFD1FAE5),
    warning = Color(0xFFC65746),
    warningBg = Color(0x1FC65746), // 12% 透明浅底
    amber = Color(0xFFD97706),
    amberBg = Color(0xFFFEF3C7),
)

val NpDarkColors = NpColors(
    bgPage = Color(0xFF151412),
    bgCard = Color(0xFF1D1B18),
    bgTertiary = Color(0xFF262320),
    bgHover = Color(0xFF2E2A26),
    bgInput = Color(0xFF262320),
    textPrimary = Color(0xFFF6F4F1),
    textSecondary = Color(0xFFC9C3BB),
    textTertiary = Color(0xFF9C958D),
    border = Color(0xFF3A3530),
    borderStrong = Color(0xFF4A453F),
    primary = Color(0xFF8B8680),
    primaryHover = Color(0xFF9A948E),
    success = Color(0xFF10B981),
    successBg = Color(0xFF16291F), // rgba(6,78,59,.3) 叠在深色底上
    warning = Color(0xFFC65746),
    warningBg = Color(0x38C65746), // 22% 透明浅底
    amber = Color(0xFFD97706),
    amberBg = Color(0xFF3A2A10),
)

val LocalNpColors = staticCompositionLocalOf { NpLightColors }

private val LightScheme = lightColorScheme(
    primary = Color(0xFF8B8680),
    onPrimary = Color(0xFFFFFFFF),
    background = Color(0xFFFAF9F5),
    onBackground = Color(0xFF2D2A26),
    surface = Color(0xFFF0EEE8),
    onSurface = Color(0xFF2D2A26),
    surfaceVariant = Color(0xFFE9E6DF),
    onSurfaceVariant = Color(0xFF6D6760),
    secondaryContainer = Color(0xFFE9E6DF),
    onSecondaryContainer = Color(0xFF6D6760),
    tertiaryContainer = Color(0xFFFEF3C7),
    onTertiaryContainer = Color(0xFFD97706),
    error = Color(0xFFC65746),
    onError = Color(0xFFFFFFFF),
    errorContainer = Color(0xFFF5E3E0),
    onErrorContainer = Color(0xFFC65746),
    outline = Color(0xFFE3E1DB),
    outlineVariant = Color(0xFFD5D2CB),
)

private val DarkScheme = darkColorScheme(
    primary = Color(0xFF8B8680),
    onPrimary = Color(0xFFFFFFFF),
    background = Color(0xFF151412),
    onBackground = Color(0xFFF6F4F1),
    surface = Color(0xFF1D1B18),
    onSurface = Color(0xFFF6F4F1),
    surfaceVariant = Color(0xFF262320),
    onSurfaceVariant = Color(0xFFC9C3BB),
    secondaryContainer = Color(0xFF262320),
    onSecondaryContainer = Color(0xFFC9C3BB),
    tertiaryContainer = Color(0xFF3A2A10),
    onTertiaryContainer = Color(0xFFD97706),
    error = Color(0xFFC65746),
    onError = Color(0xFFFFFFFF),
    errorContainer = Color(0xFF3C231E),
    onErrorContainer = Color(0xFFC65746),
    outline = Color(0xFF3A3530),
    outlineVariant = Color(0xFF4A453F),
)

private val NpShapes = Shapes(
    small = RoundedCornerShape(8.dp),
    medium = RoundedCornerShape(12.dp),
    large = RoundedCornerShape(12.dp),
)

// Web base is 14px; titles 15-16px/600; captions 11-13px.
private val NpTypography = Typography(
    headlineLarge = TextStyle(fontSize = 22.sp, fontWeight = FontWeight.SemiBold),
    titleMedium = TextStyle(fontSize = 16.sp, fontWeight = FontWeight.SemiBold),
    titleSmall = TextStyle(fontSize = 15.sp, fontWeight = FontWeight.SemiBold),
    bodyLarge = TextStyle(fontSize = 14.sp, fontWeight = FontWeight.Normal),
    bodyMedium = TextStyle(fontSize = 14.sp, fontWeight = FontWeight.Normal),
    bodySmall = TextStyle(fontSize = 13.sp, fontWeight = FontWeight.Normal),
    labelMedium = TextStyle(fontSize = 12.sp, fontWeight = FontWeight.Medium),
    labelSmall = TextStyle(fontSize = 11.sp, fontWeight = FontWeight.Normal),
)

@Composable
fun NodePanelTheme(content: @Composable () -> Unit) {
    val dark = isSystemInDarkTheme()
    CompositionLocalProvider(LocalNpColors provides if (dark) NpDarkColors else NpLightColors) {
        MaterialTheme(
            colorScheme = if (dark) DarkScheme else LightScheme,
            shapes = NpShapes,
            typography = NpTypography,
            content = content,
        )
    }
}

/** Shorthand accessor: NpTheme.colors.* inside composables. */
object NpTheme {
    val colors: NpColors
        @Composable get() = LocalNpColors.current
}
