package de.voilde.nodepanel.ui.nodes

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
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
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
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
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
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.FirewallInfo
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpEmpty
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpOutlineButton
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.components.StatusDot
import de.voilde.nodepanel.ui.theme.NpTheme
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

@Composable
fun NodesScreen(
    container: AppContainer,
    viewModel: NodesViewModel = viewModel { NodesViewModel(container) },
) {
    val state by viewModel.uiState.collectAsState()
    val snackbarHostState = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()
    var addOpen by remember { mutableStateOf(false) }

    LaunchedEffect(state.message) {
        state.message?.let {
            snackbarHostState.showSnackbar(it)
            viewModel.clearMessage()
        }
    }

    Scaffold(
        containerColor = NpTheme.colors.bgPage,
        topBar = { NpTopBar(title = "节点") },
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
                state.nodes.isEmpty() -> {
                    NpEmpty("暂无节点", modifier = Modifier.align(Alignment.Center), icon = Icons.Filled.Dns)
                }
                else -> {
                    LazyColumn(
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(16.dp),
                        verticalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        item {
                            NpButton(
                                text = "添加节点",
                                onClick = { addOpen = true },
                                modifier = Modifier.fillMaxWidth(),
                            )
                        }
                        items(state.nodes, key = { it.id }) { node ->
                            NodeCard(
                                node = node,
                                onRename = { viewModel.showRename(node) },
                                onFirewall = { viewModel.openFirewall(node) },
                                onDelete = { viewModel.showDelete(node) },
                            )
                        }
                    }
                }
            }
        }
    }

    state.renameTarget?.let { node ->
        RenameDialog(
            node = node,
            busy = state.actionInProgress,
            onConfirm = viewModel::rename,
            onDismiss = viewModel::dismissRename,
        )
    }

    state.deleteTarget?.let { node ->
        AlertDialog(
            onDismissRequest = viewModel::dismissDelete,
            title = { Text("删除节点") },
            text = { Text("确定删除节点「${node.name.ifBlank { node.id }}」吗？此操作不可恢复，节点上的 agent 将不再受管理。") },
            confirmButton = {
                TextButton(
                    onClick = viewModel::delete,
                    enabled = !state.actionInProgress,
                ) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = viewModel::dismissDelete) { Text("取消") }
            },
        )
    }

    state.firewallNode?.let { node ->
        FirewallDialog(
            node = node,
            info = state.firewall,
            loading = state.firewallLoading,
            onToggle = viewModel::toggleFirewall,
            onSetPort = viewModel::setPort,
            onDismiss = viewModel::dismissFirewall,
        )
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

@Composable
private fun NodeCard(
    node: NodeView,
    onRename: () -> Unit,
    onFirewall: () -> Unit,
    onDelete: () -> Unit,
) {
    var menuOpen by remember { mutableStateOf(false) }

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusDot(online = node.online)
                Spacer(Modifier.width(8.dp))
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        node.name.ifBlank { node.hostname.ifBlank { node.id.take(8) } },
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    if (node.ipv4.isNotBlank() || node.ipv6.isNotBlank()) {
                        Text(
                            listOf(node.ipv4, node.ipv6).filter { it.isNotBlank() }.joinToString(" / "),
                            style = MaterialTheme.typography.bodySmall,
                            color = NpTheme.colors.textSecondary,
                        )
                    }
                }
                StatusBadge(
                    text = if (node.online) "在线" else "离线",
                    kind = if (node.online) NpBadgeKind.Success else NpBadgeKind.Muted,
                )
                Box {
                    IconButton(onClick = { menuOpen = true }) {
                        Icon(Icons.Filled.MoreVert, contentDescription = "操作", tint = NpTheme.colors.textSecondary)
                    }
                    DropdownMenu(expanded = menuOpen, onDismissRequest = { menuOpen = false }) {
                        DropdownMenuItem(text = { Text("改名") }, onClick = { menuOpen = false; onRename() })
                        DropdownMenuItem(text = { Text("防火墙") }, onClick = { menuOpen = false; onFirewall() })
                        DropdownMenuItem(
                            text = { Text("删除", color = NpTheme.colors.warning) },
                            onClick = { menuOpen = false; onDelete() },
                        )
                    }
                }
            }
            Spacer(Modifier.height(6.dp))
            val details = buildList {
                if (node.os.isNotBlank()) add("${node.os}/${node.arch}".trimEnd('/'))
                if (node.country.isNotBlank()) add(node.country)
                if (node.agentVersion.isNotBlank()) add("agent ${node.agentVersion}")
                if (node.lastSeen > 0) add("最后在线 ${formatTime(node.lastSeen)}")
            }
            if (details.isNotEmpty()) {
                Text(
                    details.joinToString(" · "),
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
            }
        }
    }
}

@Composable
private fun RenameDialog(
    node: NodeView,
    busy: Boolean,
    onConfirm: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    var name by remember(node.id) { mutableStateOf(node.name) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("节点改名") },
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
            TextButton(onClick = { onConfirm(name) }, enabled = !busy && name.isNotBlank()) {
                Text("保存")
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun FirewallDialog(
    node: NodeView,
    info: FirewallInfo?,
    loading: Boolean,
    onToggle: () -> Unit,
    onSetPort: (port: String, proto: String, open: Boolean) -> Unit,
    onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("防火墙 · ${node.name.ifBlank { node.id.take(8) }}") },
        text = {
            Column {
                when {
                    loading && info == null -> {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(modifier = Modifier.height(20.dp).width(20.dp), strokeWidth = 2.dp)
                            Spacer(Modifier.width(8.dp))
                            Text("读取状态中…")
                        }
                    }
                    info != null -> {
                        Text(
                            "类型：${info.type.ifBlank { "unknown" }} · 状态：${if (info.active) "已开启" else "已关闭"}",
                            style = MaterialTheme.typography.bodyMedium,
                        )
                        info.error?.takeIf { it.isNotBlank() }?.let {
                            Spacer(Modifier.height(6.dp))
                            Text(it, color = NpTheme.colors.warning, style = MaterialTheme.typography.bodySmall)
                        }
                        Spacer(Modifier.height(10.dp))
                        NpOutlineButton(
                            text = if (info.active) "关闭防火墙" else "开启防火墙",
                            onClick = onToggle,
                            enabled = !loading,
                        )
                        val ports = info.ports.orEmpty()
                        if (ports.isNotEmpty()) {
                            Spacer(Modifier.height(12.dp))
                            Text("监听端口", style = MaterialTheme.typography.titleSmall)
                            Spacer(Modifier.height(4.dp))
                            ports.forEach { p ->
                                Row(
                                    verticalAlignment = Alignment.CenterVertically,
                                    modifier = Modifier.fillMaxWidth(),
                                ) {
                                    Text(
                                        "${p.port}/${p.proto}",
                                        style = MaterialTheme.typography.bodyMedium,
                                        modifier = Modifier.weight(1f),
                                    )
                                    StatusBadge(
                                        text = if (p.open) "已开放" else "未开放",
                                        kind = if (p.open) NpBadgeKind.Success else NpBadgeKind.Muted,
                                    )
                                    NpGhostButton(
                                        text = if (p.open) "关闭" else "开放",
                                        onClick = { onSetPort(p.port, p.proto, !p.open) },
                                        enabled = !loading,
                                    )
                                }
                            }
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = onDismiss) { Text("关闭") }
        },
    )
}

private fun formatTime(epochSec: Long): String =
    SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(epochSec * 1000))
