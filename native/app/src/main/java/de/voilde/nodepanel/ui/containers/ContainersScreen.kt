package de.voilde.nodepanel.ui.containers

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
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Search
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.ContainerView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpEmpty
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.theme.NpTheme

@Composable
fun ContainersScreen(
    container: AppContainer,
    viewModel: ContainersViewModel = viewModel { ContainersViewModel(container) },
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
        topBar = { NpTopBar(title = "容器") },
        snackbarHost = { SnackbarHost(snackbarHostState) },
    ) { padding ->
        val filtered = state.containers.filter { c ->
            val q = state.query
            q.isBlank() ||
                c.displayName.contains(q, ignoreCase = true) ||
                c.name.contains(q, ignoreCase = true) ||
                c.image.contains(q, ignoreCase = true) ||
                (state.nodeNames[c.nodeId] ?: "").contains(q, ignoreCase = true)
        }
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            OutlinedTextField(
                value = state.query,
                onValueChange = viewModel::setQuery,
                placeholder = { Text("搜索容器 / 镜像 / 节点", color = NpTheme.colors.textTertiary) },
                leadingIcon = { Icon(Icons.Filled.Search, contentDescription = null, tint = NpTheme.colors.textTertiary) },
                singleLine = true,
                shape = RoundedCornerShape(8.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedContainerColor = NpTheme.colors.bgInput,
                    unfocusedContainerColor = NpTheme.colors.bgInput,
                    focusedBorderColor = NpTheme.colors.primary,
                    unfocusedBorderColor = NpTheme.colors.borderStrong,
                ),
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
            )
            if (state.query.isNotBlank() && state.containers.isNotEmpty()) {
                Text(
                    "共 ${state.containers.size} 个 · 筛选出 ${filtered.size} 个",
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
            }
            Box(modifier = Modifier.fillMaxSize()) {
                when {
                    state.loading && state.containers.isEmpty() -> {
                        LinearProgressIndicator(
                            modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                            color = NpTheme.colors.primary,
                            trackColor = NpTheme.colors.bgTertiary,
                        )
                    }
                    state.error != null && state.containers.isEmpty() -> {
                        Column(
                            modifier = Modifier.align(Alignment.Center),
                            horizontalAlignment = Alignment.CenterHorizontally,
                        ) {
                            Text(state.error!!, color = NpTheme.colors.warning)
                            TextButton(onClick = { viewModel.refresh() }) { Text("重试") }
                        }
                    }
                    state.containers.isEmpty() -> {
                        NpEmpty(
                            text = "暂无容器",
                            icon = Icons.Filled.Storage,
                            modifier = Modifier.align(Alignment.Center),
                        )
                    }
                    filtered.isEmpty() -> {
                        NpEmpty(
                            text = "没有匹配的容器",
                            icon = Icons.Filled.Storage,
                            modifier = Modifier.align(Alignment.Center),
                        )
                    }
                    else -> {
                        LazyColumn(
                            modifier = Modifier.fillMaxSize(),
                            contentPadding = PaddingValues(16.dp),
                            verticalArrangement = Arrangement.spacedBy(12.dp),
                        ) {
                            items(
                                filtered,
                                key = { "${it.nodeId}/${it.containerId}" },
                            ) { c ->
                                ContainerCard(
                                    c = c,
                                    nodeName = state.nodeNames[c.nodeId] ?: c.nodeId.take(8),
                                    busy = state.actionInProgress,
                                    onAction = { action -> viewModel.requestAction(c, action) },
                                )
                            }
                        }
                    }
                }
            }
        }
    }

    state.pending?.let { pending ->
        AlertDialog(
            onDismissRequest = viewModel::dismissPending,
            title = { Text(pending.title) },
            text = { Text(pending.text) },
            confirmButton = {
                TextButton(onClick = viewModel::confirmPending) {
                    Text(
                        "确定",
                        color = if (pending.action == "stop") NpTheme.colors.warning
                        else NpTheme.colors.primary,
                    )
                }
            },
            dismissButton = {
                TextButton(onClick = viewModel::dismissPending) { Text("取消") }
            },
        )
    }
}

@Composable
private fun ContainerCard(
    c: ContainerView,
    nodeName: String,
    busy: Boolean,
    onAction: (String) -> Unit,
) {
    val running = c.state == "running"

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        c.displayName.ifBlank { c.name },
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        c.image,
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                        maxLines = 1,
                    )
                }
                if (c.hasUpdate == 1) {
                    StatusBadge(text = "有更新", kind = NpBadgeKind.Amber)
                    Spacer(Modifier.padding(start = 6.dp))
                }
                StatusBadge(
                    text = if (running) "运行中" else c.state.ifBlank { "已停止" },
                    kind = if (running) NpBadgeKind.Success else NpBadgeKind.Muted,
                )
            }
            Spacer(Modifier.height(6.dp))
            Text(
                buildList {
                    add("节点 $nodeName")
                    if (c.status.isNotBlank()) add(c.status)
                    if (c.hasUpdate == -1) add("更新状态未知")
                    if (c.hasUpdate == 1 && c.note.isNotBlank()) add(c.note)
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
            Row {
                if (running) {
                    NpGhostButton(text = "停止", onClick = { onAction("stop") }, enabled = !busy, danger = true)
                    NpGhostButton(text = "重启", onClick = { onAction("restart") }, enabled = !busy)
                } else {
                    NpGhostButton(text = "启动", onClick = { onAction("start") }, enabled = !busy)
                }
                if (c.hasUpdate == 1) {
                    NpGhostButton(text = "更新", onClick = { onAction("update") }, enabled = !busy)
                }
            }
        }
    }
}
