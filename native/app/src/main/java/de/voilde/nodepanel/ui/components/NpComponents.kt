package de.voilde.nodepanel.ui.components

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import de.voilde.nodepanel.ui.theme.NpTheme

// Shared primitives ported from web/src/styles/global.scss: 1px-bordered cards
// with the faintest shadow, pill badges, status dots, warm-gray buttons.

/** Card: 12dp radius, 1px border, hairline shadow (web: --radius-lg + --shadow). */
@Composable
fun NpCard(
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    Surface(
        modifier = modifier,
        shape = RoundedCornerShape(12.dp),
        color = NpTheme.colors.bgCard,
        border = BorderStroke(1.dp, NpTheme.colors.border),
        shadowElevation = 1.dp,
        tonalElevation = 0.dp,
        content = content,
    )
}

enum class NpBadgeKind { Success, Warning, Muted, Amber }

/** Pill badge (web: .badge — 999px radius, 11px 600-weight). */
@Composable
fun StatusBadge(text: String, kind: NpBadgeKind, modifier: Modifier = Modifier) {
    val (bg, fg) = when (kind) {
        NpBadgeKind.Success -> NpTheme.colors.successBg to NpTheme.colors.success
        NpBadgeKind.Warning -> NpTheme.colors.warningBg to NpTheme.colors.warning
        NpBadgeKind.Muted -> NpTheme.colors.bgTertiary to NpTheme.colors.textSecondary
        NpBadgeKind.Amber -> NpTheme.colors.amberBg to NpTheme.colors.amber
    }
    Box(
        modifier = modifier
            .clip(RoundedCornerShape(999.dp))
            .background(bg)
            .padding(horizontal = 9.dp, vertical = 2.dp),
    ) {
        Text(text, color = fg, fontSize = 11.sp, fontWeight = FontWeight.SemiBold)
    }
}

/** 8dp status dot (web: .status-dot — online green / offline gray). */
@Composable
fun StatusDot(online: Boolean, modifier: Modifier = Modifier) {
    Box(
        modifier = modifier
            .size(8.dp)
            .clip(CircleShape)
            .background(if (online) NpTheme.colors.success else NpTheme.colors.textTertiary),
    )
}

/** Primary button (web: .btn.primary — warm gray, 8dp radius, 13px 500). */
@Composable
fun NpButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    loading: Boolean = false,
) {
    Button(
        onClick = onClick,
        enabled = enabled && !loading,
        modifier = modifier,
        shape = RoundedCornerShape(8.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = NpTheme.colors.primary,
            contentColor = Color.White,
        ),
        contentPadding = ButtonDefaults.ContentPadding,
    ) {
        if (loading) {
            CircularProgressIndicator(
                modifier = Modifier.size(16.dp),
                strokeWidth = 2.dp,
                color = Color.White,
            )
        } else {
            Text(text, fontSize = 13.sp, fontWeight = FontWeight.Medium)
        }
    }
}

/** Secondary button (web: .btn — bordered, card background). */
@Composable
fun NpOutlineButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
) {
    OutlinedButton(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier,
        shape = RoundedCornerShape(8.dp),
        border = BorderStroke(1.dp, NpTheme.colors.borderStrong),
        colors = ButtonDefaults.outlinedButtonColors(
            containerColor = NpTheme.colors.bgCard,
            contentColor = NpTheme.colors.textPrimary,
        ),
    ) {
        Text(text, fontSize = 13.sp, fontWeight = FontWeight.Medium)
    }
}

/** Ghost text action; danger variant in warning red (web: .btn.ghost/.danger). */
@Composable
fun NpGhostButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    danger: Boolean = false,
) {
    TextButton(onClick = onClick, enabled = enabled, modifier = modifier) {
        Text(
            text,
            fontSize = 13.sp,
            fontWeight = FontWeight.Medium,
            color = if (danger) NpTheme.colors.warning else NpTheme.colors.textSecondary,
        )
    }
}

/** Section title (web: page h3 — 15px 600). */
@Composable
fun NpSectionHeader(text: String, modifier: Modifier = Modifier) {
    Text(
        text,
        style = MaterialTheme.typography.titleSmall,
        color = NpTheme.colors.textPrimary,
        modifier = modifier,
    )
}

/** Thin divider in border color (web: 1px list separators). */
@Composable
fun NpDivider(modifier: Modifier = Modifier) {
    Box(
        modifier = modifier
            .fillMaxWidth()
            .height(1.dp)
            .background(NpTheme.colors.border),
    )
}

/** Brand mark (web: .logo-dot — 22dp, 6dp radius, 135° warm-gray gradient, white N). */
@Composable
fun LogoDot(size: Dp = 22.dp, modifier: Modifier = Modifier) {
    Box(
        modifier = modifier
            .size(size)
            .clip(RoundedCornerShape(6.dp))
            .background(
                Brush.linearGradient(
                    listOf(NpTheme.colors.primary, NpTheme.colors.primaryHover),
                ),
            ),
        contentAlignment = Alignment.Center,
    ) {
        Text("N", color = Color.White, fontSize = (size.value * 0.6).sp, fontWeight = FontWeight.SemiBold)
    }
}

/** Top bar aligned with the web 56px header: bgCard + 1px bottom border + brand. */
@Composable
fun NpTopBar(
    title: String,
    modifier: Modifier = Modifier,
    showBrand: Boolean = true,
    navigationIcon: (@Composable () -> Unit)? = null,
    actions: (@Composable () -> Unit)? = null,
) {
    Column(modifier = modifier) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier
                .fillMaxWidth()
                .height(56.dp)
                .background(NpTheme.colors.bgCard)
                .padding(horizontal = 16.dp),
        ) {
            if (navigationIcon != null) {
                navigationIcon()
                Spacer(Modifier.width(8.dp))
            } else if (showBrand) {
                LogoDot()
                Spacer(Modifier.width(8.dp))
            }
            Text(
                title,
                fontSize = 16.sp,
                fontWeight = FontWeight.SemiBold,
                color = NpTheme.colors.textPrimary,
                modifier = Modifier.weight(1f),
            )
            actions?.invoke()
        }
        NpDivider()
    }
}

/** Centered gray empty state (icon + one-line caption). */
@Composable
fun NpEmpty(
    text: String,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
) {
    Column(
        modifier = modifier.padding(32.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        if (icon != null) {
            Icon(
                icon,
                contentDescription = null,
                tint = NpTheme.colors.textTertiary,
                modifier = Modifier.size(40.dp),
            )
            Spacer(Modifier.height(8.dp))
        }
        Text(text, color = NpTheme.colors.textTertiary, style = MaterialTheme.typography.bodyMedium)
    }
}

/**
 * Thin rounded metric bar (4-6dp): green under 60%, amber to 85%, red above.
 * value in 0f..1f.
 */
@Composable
fun NpProgressBar(
    value: Float,
    modifier: Modifier = Modifier,
    height: Dp = 5.dp,
) {
    val fraction = value.coerceIn(0f, 1f)
    val color = when {
        fraction < 0.6f -> NpTheme.colors.success
        fraction < 0.85f -> NpTheme.colors.amber
        else -> NpTheme.colors.warning
    }
    Box(
        modifier = modifier
            .fillMaxWidth()
            .height(height)
            .clip(RoundedCornerShape(999.dp))
            .background(NpTheme.colors.bgTertiary),
    ) {
        Box(
            modifier = Modifier
                .fillMaxWidth(fraction)
                .height(height)
                .clip(RoundedCornerShape(999.dp))
                .background(color),
        )
    }
}
