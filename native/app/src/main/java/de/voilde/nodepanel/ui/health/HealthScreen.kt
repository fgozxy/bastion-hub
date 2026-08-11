package de.voilde.nodepanel.ui.health

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
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.HealthAlertView
import de.voilde.nodepanel.data.api.HealthNodeStatus
import de.voilde.nodepanel.data.api.HealthSample
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpOutlineButton
import de.voilde.nodepanel.ui.components.NpProgressBar
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.theme.NpTheme
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@Composable
fun HealthScreen(
    container: AppContainer,
    onBack: () -> Unit,
    viewModel: HealthViewModel = viewModel { HealthViewModel(container) },
) {
    val state by viewModel.uiState.collectAsState()
    val snackbarHostState = remember { SnackbarHostState() }

    LaunchedEffect(state.message) {
        state.message?.let {
            snackbarHostState.showSnackbar(it)
            viewModel.clearMessage()
        }
    }

    Scaffold(
        containerColor = NpTheme.colors.bgPage,
        topBar = {
            NpTopBar(
                title = "健康监控",
                showBrand = false,
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = "返回",
                            tint = NpTheme.colors.textSecondary,
                        )
                    }
                },
            )
        },
        snackbarHost = { SnackbarHost(snackbarHostState) },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            when {
                state.loading && state.nodes.isEmpty() -> {
                    LinearProgressIndicator(
                        modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                        color = NpTheme.colors.primary,
                        trackColor = NpTheme.colors.bgTertiary,
                    )
                }
                state.error != null && state.nodes.isEmpty() -> {
                    Column(
                        modifier = Modifier.align(Alignment.Center),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        Text(state.error!!, color = NpTheme.colors.warning)
                        TextButton(onClick = { viewModel.refresh() }) { Text("重试") }
                    }
                }
                else -> {
                    LazyColumn(
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(16.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        items(state.nodes, key = { it.nodeId }) { node ->
                            NodeHealthCard(node, state, viewModel)
                        }
                        state.template?.let { tpl ->
                            item { TemplateCard(tpl) }
                        }
                    }
                }
            }
        }
    }

    state.op?.let { op ->
        AlertDialog(
            onDismissRequest = viewModel::dismissOp,
            title = { Text(if (op.install) "安装 Netdata" else "卸载 Netdata") },
            text = {
                Text(
                    if (op.install) {
                        "确定在节点「${op.node.name}」上安装 Netdata 吗？将下载并运行官方 kickstart 脚本（约 1-3 分钟），仅监听 127.0.0.1。"
                    } else {
                        "确定卸载节点「${op.node.name}」上的 Netdata 吗？卸载后该节点的健康指标与告警将停止采集。"
                    },
                )
            },
            confirmButton = {
                TextButton(onClick = viewModel::confirmOp, enabled = !state.actionInProgress) {
                    Text(
                        "确定",
                        color = if (op.install) NpTheme.colors.primary else NpTheme.colors.warning,
                    )
                }
            },
            dismissButton = {
                TextButton(onClick = viewModel::dismissOp) { Text("取消") }
            },
        )
    }

    state.alertForm?.let { form ->
        AlertFormDialog(
            form = form,
            busy = state.actionInProgress,
            onSave = viewModel::saveAlert,
            onDismiss = viewModel::closeAlertForm,
        )
    }

    state.deleteAlert?.let { alert ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteAlert(null) },
            title = { Text("删除告警规则") },
            text = { Text("确定删除 ${alert.metric} ≥ ${alert.threshold} 的告警规则吗？") },
            confirmButton = {
                TextButton(onClick = viewModel::deleteAlert, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteAlert(null) }) { Text("取消") }
            },
        )
    }
}

@Composable
private fun NodeHealthCard(
    node: HealthNodeStatus,
    state: HealthUiState,
    viewModel: HealthViewModel,
) {
    val expanded = state.expandedNodeId == node.nodeId
    val alerts = state.alerts[node.nodeId].orEmpty()

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        node.name.ifBlank { node.nodeId.take(8) },
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        buildList {
                            add(if (node.online) "在线" else "离线")
                            add(
                                when {
                                    node.installed && node.enabled -> "Netdata 已启用"
                                    node.installed -> "Netdata 已安装(停用)"
                                    else -> "Netdata 未安装"
                                },
                            )
                            if (!node.supportsHttpFetch) add("agent 过旧")
                        }.joinToString(" · "),
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                    )
                }
                StatusBadge(
                    text = if (node.installed) "监控中" else "未监控",
                    kind = if (node.installed) NpBadgeKind.Success else NpBadgeKind.Muted,
                )
            }

            node.sample?.let { sample ->
                Spacer(Modifier.height(8.dp))
                SampleRow(sample)
            }

            Row {
                if (node.installed) {
                    NpGhostButton(
                        text = "卸载",
                        onClick = { viewModel.requestOp(node, install = false) },
                        enabled = !state.actionInProgress && node.online,
                        danger = true,
                    )
                    NpGhostButton(
                        text = if (expanded) "收起告警" else "告警规则",
                        onClick = { viewModel.toggleExpand(node.nodeId) },
                    )
                } else {
                    NpGhostButton(
                        text = "安装 Netdata",
                        onClick = { viewModel.requestOp(node, install = true) },
                        enabled = !state.actionInProgress && node.online,
                    )
                }
            }

            if (expanded) {
                Spacer(Modifier.height(4.dp))
                if (alerts.isEmpty()) {
                    Text(
                        "暂无告警规则",
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textTertiary,
                    )
                }
                alerts.forEach { alert ->
                    AlertRow(
                        alert = alert,
                        busy = state.actionInProgress,
                        onEdit = { viewModel.openAlertForm(node, alert) },
                        onDelete = { viewModel.showDeleteAlert(alert) },
                    )
                }
                NpGhostButton(text = "新建规则", onClick = { viewModel.openAlertForm(node, null) })
            }
        }
    }
}

@Composable
private fun SampleRow(s: HealthSample) {
    Column {
        MetricBar("CPU", s.cpu.toFloat() / 100f, "%.0f%%".format(s.cpu))
        Spacer(Modifier.height(6.dp))
        MetricBar("内存", s.memUsedPct.toFloat() / 100f, "%.0f%%".format(s.memUsedPct))
        Spacer(Modifier.height(6.dp))
        MetricBar("磁盘", s.diskUsedPct.toFloat() / 100f, "%.0f%%".format(s.diskUsedPct))
        Spacer(Modifier.height(6.dp))
        Text(
            "Swap %.0f%% · 负载 %.2f/%.2f/%.2f · IO等待 %.1f%%".format(
                s.swapUsedPct, s.load1, s.load5, s.load15, s.iowait,
            ),
            style = MaterialTheme.typography.labelSmall,
            color = NpTheme.colors.textTertiary,
        )
        Text(
            "网络 ↓%.0f ↑%.0f KB/s · 磁盘IO 读%.0f 写%.0f KB/s · 采样 %s".format(
                s.netRx, s.netTx, s.diskRead, s.diskWrite, formatTime(s.ts),
            ),
            style = MaterialTheme.typography.labelSmall,
            color = NpTheme.colors.textTertiary,
        )
    }
}

@Composable
private fun MetricBar(label: String, value: Float, valueText: String) {
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
private fun AlertRow(
    alert: HealthAlertView,
    busy: Boolean,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                "${alert.metric} ≥ ${alert.threshold}${if (alert.windowSec > 0) " 持续 ${alert.windowSec}s" else ""}",
                style = MaterialTheme.typography.bodyMedium,
                color = NpTheme.colors.textPrimary,
            )
            if (alert.lastNotified > 0) {
                Text(
                    "上次触发 ${formatTime(alert.lastNotified)}",
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
            }
        }
        StatusBadge(
            text = if (alert.enabled) "启用" else "停用",
            kind = if (alert.enabled) NpBadgeKind.Success else NpBadgeKind.Muted,
        )
        NpGhostButton(text = "编辑", onClick = onEdit, enabled = !busy)
        NpGhostButton(text = "删除", onClick = onDelete, enabled = !busy, danger = true)
    }
}

@Composable
private fun AlertFormDialog(
    form: AlertForm,
    busy: Boolean,
    onSave: (metric: String, threshold: String, windowSec: String, enabled: Boolean) -> Unit,
    onDismiss: () -> Unit,
) {
    var metric by remember { mutableStateOf(form.editing?.metric ?: "cpu") }
    var threshold by remember { mutableStateOf(form.editing?.threshold?.toString() ?: "90") }
    var windowSec by remember { mutableStateOf(form.editing?.windowSec?.toString() ?: "300") }
    var enabled by remember { mutableStateOf(form.editing?.enabled ?: true) }
    var metricOpen by remember { mutableStateOf(false) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (form.editing == null) "新建告警规则" else "编辑告警规则") },
        text = {
            Column {
                Text(
                    "节点：${form.nodeName}",
                    style = MaterialTheme.typography.bodySmall,
                    color = NpTheme.colors.textTertiary,
                )
                Spacer(Modifier.height(8.dp))
                Box {
                    NpOutlineButton(
                        text = "指标：$metric",
                        onClick = { metricOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    DropdownMenu(expanded = metricOpen, onDismissRequest = { metricOpen = false }) {
                        ALERT_METRICS.forEach { m ->
                            DropdownMenuItem(text = { Text(m) }, onClick = { metricOpen = false; metric = m })
                        }
                    }
                }
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = threshold,
                    onValueChange = { threshold = it.filter { c -> c.isDigit() || c == '.' } },
                    label = { Text("阈值（load 填 0 = 核数×2）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = windowSec,
                    onValueChange = { windowSec = it.filter(Char::isDigit) },
                    label = { Text("持续秒数（0 = 立即触发）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("启用", modifier = Modifier.weight(1f))
                    Switch(checked = enabled, onCheckedChange = { enabled = it })
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onSave(metric, threshold, windowSec, enabled) },
                enabled = !busy && threshold.isNotBlank(),
            ) { Text("保存") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun TemplateCard(tpl: de.voilde.nodepanel.data.api.HealthTemplateResponse) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(
                "采集模板（只读）",
                style = MaterialTheme.typography.titleSmall,
                color = NpTheme.colors.textPrimary,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                "启用指标：" + tpl.template.enabled.joinToString("、") { key ->
                    tpl.catalog.firstOrNull { it.key == key }?.label ?: key
                },
                style = MaterialTheme.typography.bodySmall,
                color = NpTheme.colors.textSecondary,
            )
            if (tpl.template.alerts.isNotEmpty()) {
                Spacer(Modifier.height(4.dp))
                Text(
                    "默认告警（安装时播种到节点）：" + tpl.template.alerts.joinToString("；") {
                        "${it.metric}≥${it.threshold}" + if (it.windowSec > 0) "/${it.windowSec}s" else ""
                    },
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
            }
        }
    }
}

private fun formatTime(epochSec: Long): String {
    if (epochSec <= 0) return "-"
    return SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(epochSec * 1000))
}
