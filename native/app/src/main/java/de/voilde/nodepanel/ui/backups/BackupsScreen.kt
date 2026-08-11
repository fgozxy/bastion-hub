package de.voilde.nodepanel.ui.backups

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
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
import de.voilde.nodepanel.data.api.BackupView
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.RestoreJobView
import de.voilde.nodepanel.data.api.ScheduleView
import de.voilde.nodepanel.data.api.TargetView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpOutlineButton
import de.voilde.nodepanel.ui.components.NpSectionHeader
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.theme.NpTheme
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@Composable
fun BackupsScreen(
    container: AppContainer,
    viewModel: BackupsViewModel = viewModel { BackupsViewModel(container) },
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
        topBar = { NpTopBar(title = "备份") },
        snackbarHost = { SnackbarHost(snackbarHostState) },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            when {
                state.loading && state.backups.isEmpty() -> {
                    LinearProgressIndicator(
                        modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                        color = NpTheme.colors.primary,
                        trackColor = NpTheme.colors.bgTertiary,
                    )
                }
                state.error != null && state.backups.isEmpty() -> {
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
                        item {
                            NpButton(
                                text = "立即备份",
                                onClick = { viewModel.showBackupNow(true) },
                                modifier = Modifier.fillMaxWidth(),
                            )
                        }

                        item { NpSectionHeader("备份列表") }
                        if (state.backups.isEmpty()) {
                            item {
                                Text(
                                    "暂无备份",
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = NpTheme.colors.textTertiary,
                                )
                            }
                        }
                        items(state.backups, key = { "b-${it.id}" }) { backup ->
                            BackupCard(
                                backup = backup,
                                nodeName = viewModel.nodeName(backup.nodeId),
                                busy = state.actionInProgress,
                                onRestore = { viewModel.showRestore(backup) },
                                onDelete = { viewModel.showDeleteBackup(backup) },
                            )
                        }

                        item {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                NpSectionHeader("定时任务", modifier = Modifier.weight(1f))
                                NpGhostButton(text = "新建", onClick = { viewModel.openScheduleForm(null) })
                            }
                        }
                        if (state.schedules.isEmpty()) {
                            item {
                                Text(
                                    "暂无定时任务",
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = NpTheme.colors.textTertiary,
                                )
                            }
                        }
                        items(state.schedules, key = { "s-${it.id}" }) { schedule ->
                            ScheduleCard(
                                schedule = schedule,
                                nodeName = viewModel.nodeName(schedule.nodeId),
                                onToggle = { viewModel.toggleSchedule(schedule) },
                                onEdit = { viewModel.openScheduleForm(schedule) },
                                onDelete = { viewModel.showDeleteSchedule(schedule) },
                            )
                        }

                        if (state.restoreJobs.isNotEmpty()) {
                            item { NpSectionHeader("恢复任务") }
                            items(state.restoreJobs, key = { "j-${it.id}" }) { job ->
                                RestoreJobCard(job, viewModel)
                            }
                        }
                    }
                }
            }
        }
    }

    if (state.showBackupNow) {
        BackupNowDialog(
            nodes = state.nodes,
            targets = state.targets,
            busy = state.actionInProgress,
            onConfirm = viewModel::backupNow,
            onDismiss = { viewModel.showBackupNow(false) },
        )
    }

    state.restoreTarget?.let { backup ->
        RestoreDialog(
            backup = backup,
            nodes = state.nodes,
            busy = state.actionInProgress,
            onConfirm = viewModel::restore,
            onDismiss = { viewModel.showRestore(null) },
        )
    }

    state.deleteBackupTarget?.let { backup ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteBackup(null) },
            title = { Text("删除备份") },
            text = { Text("确定删除备份「${backup.name.ifBlank { backup.id }}」吗？本地暂存与远端目标中的存档都会被删除，不可恢复。") },
            confirmButton = {
                TextButton(onClick = viewModel::deleteBackup, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteBackup(null) }) { Text("取消") }
            },
        )
    }

    state.scheduleForm?.let { form ->
        ScheduleFormDialog(
            initial = form,
            nodes = state.nodes,
            targets = state.targets,
            busy = state.actionInProgress,
            onSave = viewModel::saveSchedule,
            onDismiss = viewModel::closeScheduleForm,
        )
    }

    state.deleteScheduleTarget?.let { schedule ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteSchedule(null) },
            title = { Text("删除定时任务") },
            text = { Text("确定删除该${if (schedule.type == "backup") "备份" else "容器更新"}计划（${schedule.cron}）吗？") },
            confirmButton = {
                TextButton(onClick = viewModel::deleteSchedule, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteSchedule(null) }) { Text("取消") }
            },
        )
    }
}

@Composable
private fun BackupCard(
    backup: BackupView,
    nodeName: String,
    busy: Boolean,
    onRestore: () -> Unit,
    onDelete: () -> Unit,
) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        backup.name.ifBlank { backup.containerName.ifBlank { backup.id.take(8) } },
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        if (backup.containerName.isNotBlank()) "容器 ${backup.containerName}"
                        else backup.paths,
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                        maxLines = 1,
                    )
                }
                StatusBadge(
                    text = when (backup.status) {
                        "ok" -> "成功"
                        "failed" -> "失败"
                        else -> backup.status.ifBlank { "进行中" }
                    },
                    kind = when (backup.status) {
                        "ok" -> NpBadgeKind.Success
                        "failed" -> NpBadgeKind.Warning
                        else -> NpBadgeKind.Muted
                    },
                )
            }
            Spacer(Modifier.height(6.dp))
            Text(
                buildList {
                    add("节点 $nodeName")
                    if (backup.size > 0) add(formatBytes(backup.size))
                    add(formatTime(backup.createdAt))
                    if (backup.error.isNotBlank()) add("错误：${backup.error}")
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
            Row {
                NpGhostButton(text = "恢复", onClick = onRestore, enabled = !busy && backup.status == "ok")
                NpGhostButton(text = "删除", onClick = onDelete, enabled = !busy, danger = true)
            }
        }
    }
}

@Composable
private fun ScheduleCard(
    schedule: ScheduleView,
    nodeName: String,
    onToggle: () -> Unit,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(12.dp),
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    if (schedule.type == "backup") "备份计划" else "容器更新计划",
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                )
                Text(
                    buildList {
                        add("节点 $nodeName")
                        add("cron ${schedule.cron}")
                        if (schedule.nextRun > 0 && schedule.enabled) add("下次 ${formatTime(schedule.nextRun)}")
                        if (schedule.lastRun > 0) add("上次 ${formatTime(schedule.lastRun)}")
                    }.joinToString(" · "),
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
                Row {
                    NpGhostButton(text = "编辑", onClick = onEdit)
                    NpGhostButton(text = "删除", onClick = onDelete, danger = true)
                }
            }
            Switch(checked = schedule.enabled, onCheckedChange = { onToggle() })
        }
    }
}

@Composable
private fun RestoreJobCard(job: RestoreJobView, viewModel: BackupsViewModel) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(
                job.container.ifBlank { job.backupId.take(8) },
                style = MaterialTheme.typography.titleSmall,
                color = NpTheme.colors.textPrimary,
            )
            Text(
                buildList {
                    add("${job.originNodeName.ifBlank { viewModel.nodeName(job.originNode) }} → ${job.targetNodeName.ifBlank { viewModel.nodeName(job.targetNode) }}")
                    add(
                        when (job.status) {
                            "ok" -> "成功"
                            "partial" -> "部分完成"
                            "failed" -> "失败"
                            "running" -> "进行中 ${job.percent}%"
                            else -> job.status
                        },
                    )
                    if (job.error.isNotBlank()) add("错误：${job.error}")
                    add(formatTime(job.startedAt))
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
        }
    }
}

@Composable
private fun NodeDropdown(
    label: String,
    nodes: List<NodeView>,
    selectedId: String,
    onlineOnly: Boolean = false,
    onSelect: (String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    val candidates = if (onlineOnly) nodes.filter { it.online } else nodes
    val selected = candidates.firstOrNull { it.id == selectedId }

    Box {
        NpOutlineButton(
            text = selected?.let { it.name.ifBlank { it.hostname.ifBlank { it.id.take(8) } } } ?: label,
            onClick = { open = true },
            modifier = Modifier.fillMaxWidth(),
        )
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            candidates.forEach { node ->
                DropdownMenuItem(
                    text = {
                        Text(node.name.ifBlank { node.hostname.ifBlank { node.id.take(8) } } + if (node.online) "" else "（离线）")
                    },
                    onClick = { open = false; onSelect(node.id) },
                )
            }
        }
    }
}

@Composable
private fun BackupNowDialog(
    nodes: List<NodeView>,
    targets: List<TargetView>,
    busy: Boolean,
    onConfirm: (nodeId: String, paths: String, name: String, targetIds: Set<String>) -> Unit,
    onDismiss: () -> Unit,
) {
    var nodeId by remember { mutableStateOf("") }
    var paths by remember { mutableStateOf("") }
    var name by remember { mutableStateOf("") }
    var targetIds by remember { mutableStateOf(setOf<String>()) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("立即备份") },
        text = {
            Column {
                NodeDropdown("选择节点", nodes, nodeId, onlineOnly = true, onSelect = { nodeId = it })
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = paths,
                    onValueChange = { paths = it },
                    label = { Text("备份路径（逗号分隔，如 /data,/etc/app）") },
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("名称（可选）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                if (targets.isNotEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Text("推送到目标（可选）", style = MaterialTheme.typography.labelMedium)
                    targets.forEach { t ->
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Checkbox(
                                checked = t.id in targetIds,
                                onCheckedChange = { checked ->
                                    targetIds = if (checked) targetIds + t.id else targetIds - t.id
                                },
                            )
                            Text("${t.name}（${t.type}）", style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onConfirm(nodeId, paths, name, targetIds) },
                enabled = !busy && nodeId.isNotBlank() && paths.isNotBlank(),
            ) { Text("开始备份") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun RestoreDialog(
    backup: BackupView,
    nodes: List<NodeView>,
    busy: Boolean,
    onConfirm: (nodeId: String, dest: String) -> Unit,
    onDismiss: () -> Unit,
) {
    var nodeId by remember { mutableStateOf(backup.nodeId) }
    var dest by remember { mutableStateOf("") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("恢复备份") },
        text = {
            Column {
                Text(
                    "将「${backup.name.ifBlank { backup.id.take(8) }}」解压到目标节点的指定路径。同名文件会被覆盖。",
                    style = MaterialTheme.typography.bodySmall,
                )
                Spacer(Modifier.height(8.dp))
                NodeDropdown("选择目标节点", nodes, nodeId, onlineOnly = true, onSelect = { nodeId = it })
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = dest,
                    onValueChange = { dest = it },
                    label = { Text("恢复目标路径（如 /var/lib/restore）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onConfirm(nodeId, dest) },
                enabled = !busy && nodeId.isNotBlank() && dest.isNotBlank(),
            ) { Text("确认恢复", color = NpTheme.colors.warning) }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ScheduleFormDialog(
    initial: ScheduleForm,
    nodes: List<NodeView>,
    targets: List<TargetView>,
    busy: Boolean,
    onSave: (ScheduleForm) -> Unit,
    onDismiss: () -> Unit,
) {
    var type by remember { mutableStateOf(initial.type) }
    var nodeId by remember { mutableStateOf(initial.nodeId) }
    var paths by remember { mutableStateOf(initial.paths) }
    var label by remember { mutableStateOf(initial.label) }
    var name by remember { mutableStateOf(initial.name) }
    var targetIds by remember { mutableStateOf(initial.targetIds) }
    var days by remember { mutableStateOf(initial.days) }
    var hour by remember { mutableStateOf(initial.hour) }
    var minute by remember { mutableStateOf(initial.minute) }
    var enabled by remember { mutableStateOf(initial.enabled) }

    val dayNames = listOf("日", "一", "二", "三", "四", "五", "六")

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (initial.editing == null) "新建定时任务" else "编辑定时任务") },
        text = {
            Column(modifier = Modifier.verticalScroll(rememberScrollState())) {
                Row {
                    FilterChip(
                        selected = type == "backup",
                        onClick = { type = "backup" },
                        label = { Text("备份") },
                    )
                    Spacer(Modifier.width(8.dp))
                    FilterChip(
                        selected = type == "container_update",
                        onClick = { type = "container_update" },
                        label = { Text("容器更新") },
                    )
                }
                Spacer(Modifier.height(8.dp))
                NodeDropdown("选择节点", nodes, nodeId, onSelect = { nodeId = it })
                if (type == "backup") {
                    Spacer(Modifier.height(8.dp))
                    OutlinedTextField(
                        value = paths,
                        onValueChange = { paths = it },
                        label = { Text("备份路径（逗号分隔）") },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Spacer(Modifier.height(8.dp))
                    OutlinedTextField(
                        value = name,
                        onValueChange = { name = it },
                        label = { Text("备份名称（可选）") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    if (targets.isNotEmpty()) {
                        Spacer(Modifier.height(8.dp))
                        Text("推送目标", style = MaterialTheme.typography.labelMedium)
                        targets.forEach { t ->
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                Checkbox(
                                    checked = t.id in targetIds,
                                    onCheckedChange = { checked ->
                                        targetIds = if (checked) targetIds + t.id else targetIds - t.id
                                    },
                                )
                                Text("${t.name}（${t.type}）", style = MaterialTheme.typography.bodyMedium)
                            }
                        }
                    }
                } else {
                    Spacer(Modifier.height(8.dp))
                    OutlinedTextField(
                        value = label,
                        onValueChange = { label = it },
                        label = { Text("容器 label 过滤（可选，空=全部）") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Spacer(Modifier.height(8.dp))
                Text("执行时间", style = MaterialTheme.typography.labelMedium)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(
                        value = hour,
                        onValueChange = { hour = it.filter(Char::isDigit).take(2) },
                        label = { Text("时") },
                        singleLine = true,
                        modifier = Modifier.width(72.dp),
                    )
                    Spacer(Modifier.width(8.dp))
                    OutlinedTextField(
                        value = minute,
                        onValueChange = { minute = it.filter(Char::isDigit).take(2) },
                        label = { Text("分") },
                        singleLine = true,
                        modifier = Modifier.width(72.dp),
                    )
                }
                Spacer(Modifier.height(4.dp))
                Text("星期（不选 = 每天）", style = MaterialTheme.typography.labelMedium)
                FlowRow {
                    dayNames.forEachIndexed { dow, dayName ->
                        FilterChip(
                            selected = dow in days,
                            onClick = {
                                days = if (dow in days) days - dow else days + dow
                            },
                            label = { Text(dayName) },
                            modifier = Modifier.padding(end = 4.dp),
                        )
                    }
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("启用", modifier = Modifier.weight(1f))
                    Switch(checked = enabled, onCheckedChange = { enabled = it })
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    onSave(
                        initial.copy(
                            type = type, nodeId = nodeId, paths = paths, label = label,
                            name = name, targetIds = targetIds, days = days,
                            hour = hour, minute = minute, enabled = enabled,
                        ),
                    )
                },
                enabled = !busy,
            ) { Text("保存") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
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
