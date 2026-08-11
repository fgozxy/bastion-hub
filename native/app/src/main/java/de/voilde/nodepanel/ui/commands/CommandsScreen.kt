package de.voilde.nodepanel.ui.commands

import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.FilterChip
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.CommandView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpSectionHeader
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.components.StatusDot
import de.voilde.nodepanel.ui.theme.NpTheme
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

// Terminal output uses fixed dark colors regardless of theme (web: .term-out).
private val TermBg = Color(0xFF1D1B18)
private val TermFg = Color(0xFFF6F4F1)

@Composable
fun CommandsScreen(
    container: AppContainer,
    viewModel: CommandsViewModel = viewModel { CommandsViewModel(container) },
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
        topBar = { NpTopBar(title = "命令") },
        snackbarHost = { SnackbarHost(snackbarHostState) },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            if (state.loading && state.history.isEmpty()) {
                LinearProgressIndicator(
                    modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                    color = NpTheme.colors.primary,
                    trackColor = NpTheme.colors.bgTertiary,
                )
            } else {
                LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    item { RunCard(state, viewModel) }

                    state.active?.let { active ->
                        item {
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                NpSectionHeader("命令输出", modifier = Modifier.weight(1f))
                                NpGhostButton(text = "关闭", onClick = viewModel::closeActive)
                            }
                        }
                        item { CommandMetaCard(active) }
                        items(active.nodeIds, key = { "out-$it" }) { nodeId ->
                            NodeOutputCard(active, nodeId, viewModel)
                        }
                    }

                    item { NpSectionHeader("历史") }
                    if (state.history.isEmpty()) {
                        item {
                            Text(
                                "暂无历史",
                                style = MaterialTheme.typography.bodyMedium,
                                color = NpTheme.colors.textTertiary,
                            )
                        }
                    }
                    items(state.history, key = { "h-${it.id}" }) { cmd ->
                        HistoryCard(cmd, viewModel)
                    }
                }
            }
        }
    }

    if (state.confirmRun) {
        val names = state.form.nodeIds.map(viewModel::nodeName)
        AlertDialog(
            onDismissRequest = viewModel::cancelRun,
            title = { Text("确认执行") },
            text = {
                Column {
                    Text("将在以下 ${names.size} 个节点执行：", style = MaterialTheme.typography.bodyMedium)
                    Text(names.joinToString("、"), style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(8.dp))
                    SelectionContainer {
                        Text(
                            state.form.cmd,
                            style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace),
                        )
                    }
                }
            },
            confirmButton = {
                TextButton(onClick = viewModel::confirmRun, enabled = !state.actionInProgress) {
                    Text("执行", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = viewModel::cancelRun) { Text("取消") }
            },
        )
    }
}

@Composable
private fun RunCard(state: CommandsUiState, viewModel: CommandsViewModel) {
    var saveDialogOpen by remember { mutableStateOf(false) }

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            OutlinedTextField(
                value = state.form.cmd,
                onValueChange = viewModel::setCmd,
                label = { Text("命令") },
                minLines = 2,
                modifier = Modifier.fillMaxWidth(),
            )
            if (state.saved.isNotEmpty()) {
                Spacer(Modifier.height(6.dp))
                Row(modifier = Modifier.horizontalScroll(rememberScrollState())) {
                    state.saved.forEach { saved ->
                        FilterChip(
                            selected = false,
                            onClick = { viewModel.applySaved(saved) },
                            label = { Text(saved.name) },
                            modifier = Modifier.padding(end = 6.dp),
                        )
                    }
                }
            }
            Spacer(Modifier.height(6.dp))
            Text(
                "目标节点",
                style = MaterialTheme.typography.labelMedium,
                color = NpTheme.colors.textSecondary,
            )
            state.nodes.filter { it.online }.forEach { node ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(
                        checked = node.id in state.form.nodeIds,
                        onCheckedChange = { viewModel.toggleNode(node.id) },
                    )
                    Text(
                        node.name.ifBlank { node.hostname.ifBlank { node.id.take(8) } },
                        style = MaterialTheme.typography.bodyMedium,
                        color = NpTheme.colors.textPrimary,
                    )
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                OutlinedTextField(
                    value = state.form.timeout,
                    onValueChange = { viewModel.setTimeout(it.filter(Char::isDigit)) },
                    label = { Text("超时(秒)") },
                    singleLine = true,
                    modifier = Modifier.width(110.dp),
                )
                Spacer(Modifier.weight(1f))
                NpGhostButton(
                    text = "存为常用",
                    onClick = { saveDialogOpen = true },
                    enabled = state.form.cmd.isNotBlank(),
                )
                NpButton(
                    text = "执行",
                    onClick = viewModel::requestRun,
                    enabled = !state.actionInProgress && state.form.cmd.isNotBlank() && state.form.nodeIds.isNotEmpty(),
                )
            }
        }
    }

    if (saveDialogOpen) {
        var name by remember { mutableStateOf("") }
        AlertDialog(
            onDismissRequest = { saveDialogOpen = false },
            title = { Text("存为常用命令") },
            text = {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("名称") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
            },
            confirmButton = {
                TextButton(
                    onClick = { viewModel.saveCurrentAs(name); saveDialogOpen = false },
                    enabled = name.isNotBlank(),
                ) { Text("保存") }
            },
            dismissButton = {
                TextButton(onClick = { saveDialogOpen = false }) { Text("取消") }
            },
        )
    }
}

@Composable
private fun CommandMetaCard(active: ActiveCommand) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            SelectionContainer {
                Text(
                    active.cmd,
                    style = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                    color = NpTheme.colors.textPrimary,
                )
            }
            Spacer(Modifier.height(4.dp))
            val statusText = when {
                active.status.startsWith("history:") -> "历史 · ${active.status.removePrefix("history:")}"
                active.status.isBlank() -> "运行中…"
                else -> "已结束 · ${active.status}"
            }
            Text(
                statusText,
                style = MaterialTheme.typography.labelSmall,
                color = if (active.status == "failed" || active.status == "history:failed") {
                    NpTheme.colors.warning
                } else {
                    NpTheme.colors.textTertiary
                },
            )
        }
    }
}

@Composable
private fun NodeOutputCard(active: ActiveCommand, nodeId: String, viewModel: CommandsViewModel) {
    val lines = active.lines[nodeId].orEmpty()
    val exit = active.exits[nodeId]

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(online = exit == null || exit == 0)
                Spacer(Modifier.width(8.dp))
                Text(
                    viewModel.nodeName(nodeId),
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                    modifier = Modifier.weight(1f),
                )
                StatusBadge(
                    text = when {
                        exit == null -> "运行中"
                        exit == 0 -> "成功"
                        else -> "exit $exit"
                    },
                    kind = when {
                        exit == null -> NpBadgeKind.Muted
                        exit == 0 -> NpBadgeKind.Success
                        else -> NpBadgeKind.Warning
                    },
                )
            }
            if (lines.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(TermBg)
                        .padding(10.dp),
                ) {
                    SelectionContainer {
                        Text(
                            lines.joinToString("") { it.data },
                            style = MaterialTheme.typography.bodySmall.copy(
                                fontFamily = FontFamily.Monospace,
                                fontSize = 12.sp,
                            ),
                            color = TermFg,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun HistoryCard(cmd: CommandView, viewModel: CommandsViewModel) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    cmd.cmd,
                    style = MaterialTheme.typography.bodyMedium.copy(fontFamily = FontFamily.Monospace),
                    color = NpTheme.colors.textPrimary,
                    maxLines = 2,
                    modifier = Modifier.weight(1f),
                )
                StatusBadge(
                    text = cmd.status,
                    kind = when (cmd.status) {
                        "completed" -> NpBadgeKind.Success
                        "failed" -> NpBadgeKind.Warning
                        else -> NpBadgeKind.Muted
                    },
                )
            }
            Spacer(Modifier.height(4.dp))
            Text(
                buildList {
                    val names = viewModel.parseNodeIds(cmd.nodeIds).map(viewModel::nodeName)
                    if (names.isNotEmpty()) add(names.joinToString("、"))
                    add(formatTime(cmd.createdAt))
                    if (cmd.exitCode != 0) add("exit ${cmd.exitCode}")
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
            Row {
                NpGhostButton(text = "查看输出", onClick = { viewModel.openHistory(cmd) })
            }
        }
    }
}

private fun formatTime(epochSec: Long): String {
    if (epochSec <= 0) return "-"
    return SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(epochSec * 1000))
}
