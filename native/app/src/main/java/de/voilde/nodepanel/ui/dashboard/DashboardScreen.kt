package de.voilde.nodepanel.ui.dashboard

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.MonitorHeart
import androidx.compose.material.icons.filled.Save
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.pulltorefresh.PullToRefreshContainer
import androidx.compose.material3.pulltorefresh.rememberPullToRefreshState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.MiniMetric
import de.voilde.nodepanel.data.api.RecentCommand
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpDivider
import de.voilde.nodepanel.ui.components.NpProgressBar
import de.voilde.nodepanel.ui.components.NpSectionHeader
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.nodes.AddNodeDialog
import de.voilde.nodepanel.ui.theme.NpTheme
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DashboardScreen(
    container: AppContainer,
    onCheckUpdate: () -> Unit = {},
    onOpenCloudflare: () -> Unit = {},
    onOpenHealth: () -> Unit = {},
    onOpenSettings: () -> Unit = {},
    currentVersion: String = "",
    viewModel: DashboardViewModel = viewModel { DashboardViewModel(container) },
) {
    val state by viewModel.uiState.collectAsState()
    val snackbarHostState = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var addOpen by remember { mutableStateOf(false) }
    val refreshState = rememberPullToRefreshState()
    if (refreshState.isRefreshing) {
        LaunchedEffect(true) { viewModel.refresh() }
    }
    if (!state.loading) {
        LaunchedEffect(true) { refreshState.endRefresh() }
    }

    Scaffold(
        containerColor = NpTheme.colors.bgPage,
        snackbarHost = { SnackbarHost(snackbarHostState) },
        topBar = {
            NpTopBar(
                title = "仪表盘",
                actions = {
                    if (currentVersion.isNotBlank()) {
                        Text(
                            "v$currentVersion",
                            style = MaterialTheme.typography.labelMedium,
                            color = NpTheme.colors.textTertiary,
                        )
                    }
                    IconButton(onClick = { addOpen = true }) {
                        Icon(Icons.Filled.Add, contentDescription = "添加节点", tint = NpTheme.colors.textSecondary)
                    }
                    IconButton(onClick = onOpenSettings) {
                        Icon(Icons.Filled.Settings, contentDescription = "设置", tint = NpTheme.colors.textSecondary)
                    }
                    IconButton(onClick = onOpenHealth) {
                        Icon(Icons.Filled.MonitorHeart, contentDescription = "健康监控", tint = NpTheme.colors.textSecondary)
                    }
                    IconButton(onClick = onOpenCloudflare) {
                        Icon(Icons.Filled.Cloud, contentDescription = "Cloudflare", tint = NpTheme.colors.textSecondary)
                    }
                    IconButton(onClick = onCheckUpdate) {
                        Icon(Icons.Filled.SystemUpdate, contentDescription = "检查更新", tint = NpTheme.colors.textSecondary)
                    }
                },
            )
        },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .nestedScroll(refreshState.nestedScrollConnection),
        ) {
            when {
                state.loading && state.stats == null -> {
                    LinearProgressIndicator(
                        modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                        color = NpTheme.colors.primary,
                        trackColor = NpTheme.colors.bgTertiary,
                    )
                }
                state.error != null && state.stats == null -> {
                    Column(
                        modifier = Modifier.align(Alignment.Center),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        Text(state.error!!, color = NpTheme.colors.warning)
                        TextButton(onClick = { viewModel.refresh() }) { Text("重试") }
                    }
                }
                state.stats != null -> {
                    val stats = state.stats!!
                    LazyColumn(
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(16.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        item {
                            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                                StatCard(
                                    title = "节点在线",
                                    value = "${stats.nodes.online} / ${stats.nodes.total}",
                                    icon = Icons.Filled.Dns,
                                    modifier = Modifier.weight(1f),
                                )
                                StatCard(
                                    title = "今日命令",
                                    value = "${stats.commands.today}",
                                    icon = Icons.Filled.Terminal,
                                    modifier = Modifier.weight(1f),
                                )
                                StatCard(
                                    title = "备份 成功/失败",
                                    value = "${stats.backups.success} / ${stats.backups.failed}",
                                    icon = Icons.Filled.Save,
                                    modifier = Modifier.weight(1f),
                                )
                            }
                        }

                        val metrics = stats.metrics.orEmpty()
                        if (metrics.isNotEmpty()) {
                            item { NpSectionHeader("节点状态") }
                            items(metrics.toList()) { (nodeId, m) ->
                                NodeMetricCard(
                                    name = state.nodeNames[nodeId] ?: nodeId.take(8),
                                    metric = m,
                                )
                            }
                        }

                        val recent = stats.recent.orEmpty()
                        if (recent.isNotEmpty()) {
                            item { NpSectionHeader("最近命令") }
                            item {
                                NpCard(modifier = Modifier.fillMaxWidth()) {
                                    Column {
                                        recent.forEachIndexed { i, cmd ->
                                            if (i > 0) NpDivider()
                                            RecentCommandRow(cmd)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
            PullToRefreshContainer(
                state = refreshState,
                modifier = Modifier.align(Alignment.TopCenter),
            )
        }
    }

    if (addOpen) {
        AddNodeDialog(
            container = container,
            onDismiss = { addOpen = false },
            onCreated = { viewModel.refresh() },
            onCopied = {
                scope.launch { snackbarHostState.showSnackbar("已复制，到目标主机以 root 执行") }
            },
        )
    }
}

/** Big number + small caption + icon, per web Dashboard stat cards. */
@Composable
private fun StatCard(
    title: String,
    value: String,
    icon: ImageVector,
    modifier: Modifier = Modifier,
) {
    NpCard(modifier = modifier) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    title,
                    style = MaterialTheme.typography.labelMedium,
                    color = NpTheme.colors.textSecondary,
                    modifier = Modifier.weight(1f),
                )
                Icon(
                    icon,
                    contentDescription = null,
                    tint = NpTheme.colors.textTertiary,
                    modifier = Modifier.width(16.dp),
                )
            }
            Spacer(Modifier.height(6.dp))
            Text(
                value,
                fontSize = 22.sp,
                fontWeight = FontWeight.SemiBold,
                color = NpTheme.colors.textPrimary,
            )
        }
    }
}

/** CPU / memory bars with threshold colors + load text. */
@Composable
private fun NodeMetricCard(name: String, metric: MiniMetric) {
    val memPct = if (metric.memTotal > 0) {
        metric.memUsed.toFloat() / metric.memTotal.toFloat()
    } else {
        0f
    }
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(name, style = MaterialTheme.typography.titleSmall, color = NpTheme.colors.textPrimary)
            Spacer(Modifier.height(10.dp))
            MetricBarRow("CPU", metric.cpu.toFloat() / 100f, "%.1f%%".format(metric.cpu))
            Spacer(Modifier.height(8.dp))
            MetricBarRow(
                "内存",
                memPct,
                "${formatBytes(metric.memUsed)} / ${formatBytes(metric.memTotal)}",
            )
            Spacer(Modifier.height(8.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    "负载",
                    style = MaterialTheme.typography.labelMedium,
                    color = NpTheme.colors.textSecondary,
                    modifier = Modifier.width(40.dp),
                )
                Text(
                    "%.2f".format(metric.load1),
                    style = MaterialTheme.typography.bodySmall,
                    color = if (metric.load1 >= 4) NpTheme.colors.warning else NpTheme.colors.textPrimary,
                )
            }
        }
    }
}

@Composable
private fun MetricBarRow(label: String, value: Float, valueText: String) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            label,
            style = MaterialTheme.typography.labelMedium,
            color = NpTheme.colors.textSecondary,
            modifier = Modifier.width(40.dp),
        )
        NpProgressBar(value = value, modifier = Modifier.weight(1f))
        Spacer(Modifier.width(8.dp))
        Text(
            valueText,
            style = MaterialTheme.typography.labelSmall,
            color = NpTheme.colors.textSecondary,
        )
    }
}

@Composable
private fun RecentCommandRow(cmd: RecentCommand) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                cmd.cmd,
                style = MaterialTheme.typography.bodySmall,
                color = NpTheme.colors.textPrimary,
                maxLines = 1,
            )
            Text(
                formatTime(cmd.at),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
        }
        StatusBadge(
            text = cmd.status,
            kind = if (cmd.status == "done" || cmd.status == "ok" || cmd.status == "completed") {
                NpBadgeKind.Success
            } else {
                NpBadgeKind.Warning
            },
        )
    }
}

private fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    var value = bytes.toDouble()
    var unit = 0
    while (value >= 1024 && unit < units.lastIndex) {
        value /= 1024
        unit++
    }
    return "%.1f %s".format(value, units[unit])
}

private fun formatTime(epochSec: Long): String {
    if (epochSec <= 0) return "-"
    return SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(epochSec * 1000))
}
