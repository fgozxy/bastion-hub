package de.voilde.nodepanel.ui.cloudflare

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
import androidx.compose.foundation.verticalScroll
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
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
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
import de.voilde.nodepanel.data.api.DnsRecord
import de.voilde.nodepanel.data.api.DnsRecordRequest
import de.voilde.nodepanel.data.api.DnsZone
import de.voilde.nodepanel.data.api.DomainRule
import de.voilde.nodepanel.data.api.DomainTunnelView
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.TunnelView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpOutlineButton
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.theme.NpTheme

@Composable
fun CloudflareScreen(
    container: AppContainer,
    onBack: () -> Unit,
    viewModel: CloudflareViewModel = viewModel { CloudflareViewModel(container) },
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
                title = "Cloudflare",
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
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            TabRow(
                selectedTabIndex = state.tab,
                containerColor = NpTheme.colors.bgCard,
            ) {
                Tab(selected = state.tab == 0, onClick = { viewModel.setTab(0) }, text = { Text("隧道") })
                Tab(selected = state.tab == 1, onClick = { viewModel.setTab(1) }, text = { Text("域名") })
                Tab(selected = state.tab == 2, onClick = { viewModel.setTab(2) }, text = { Text("DNS") })
            }
            when (state.tab) {
                0 -> TunnelsTab(state, viewModel)
                1 -> DomainsTab(state, viewModel)
                else -> DnsTab(state, viewModel)
            }
        }
    }

    // --- dialogs ---

    if (state.showCreateTunnel) {
        CreateTunnelDialog(
            nodes = state.nodes,
            busy = state.actionInProgress,
            onConfirm = viewModel::createTunnel,
            onDismiss = { viewModel.showCreateTunnel(false) },
        )
    }

    state.renameTunnel?.let { tunnel ->
        var name by remember(tunnel.id) { mutableStateOf(tunnel.name) }
        AlertDialog(
            onDismissRequest = { viewModel.showRenameTunnel(null) },
            title = { Text("重命名隧道") },
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
                    onClick = { viewModel.renameTunnel(name) },
                    enabled = !state.actionInProgress && name.isNotBlank(),
                ) { Text("保存") }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showRenameTunnel(null) }) { Text("取消") }
            },
        )
    }

    state.deleteTunnel?.let { tunnel ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteTunnel(null) },
            title = { Text("删除隧道") },
            text = {
                Text("确定删除隧道「${tunnel.name}」吗？将停止节点上的 cloudflared、删除 CF 隧道并清理相关 DNS CNAME 记录，不可恢复。")
            },
            confirmButton = {
                TextButton(onClick = viewModel::deleteTunnel, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteTunnel(null) }) { Text("取消") }
            },
        )
    }

    state.ruleForm?.let { form ->
        RuleFormDialog(
            form = form,
            busy = state.actionInProgress,
            onSave = viewModel::saveRule,
            onDismiss = viewModel::closeRuleForm,
        )
    }

    state.deleteRule?.let { (tunnel, rule) ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteRule(null) },
            title = { Text("删除域名规则") },
            text = {
                Text("确定从隧道「${tunnel.name}」删除「${rule.hostname}${rule.path.ifBlank { "" }}」的规则吗？该域名将落到 catch-all（404）。DNS 记录不会被删除。")
            },
            confirmButton = {
                TextButton(onClick = viewModel::deleteRule, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteRule(null) }) { Text("取消") }
            },
        )
    }

    state.moveForm?.let { form ->
        MoveDialog(
            form = form,
            tunnels = state.domainTunnels,
            busy = state.actionInProgress,
            onConfirm = viewModel::moveRule,
            onDismiss = viewModel::closeMoveForm,
        )
    }

    state.recordForm?.let { form ->
        RecordFormDialog(
            form = form,
            busy = state.actionInProgress,
            onSave = viewModel::saveRecord,
            onDismiss = viewModel::closeRecordForm,
        )
    }

    state.deleteRecord?.let { record ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteRecord(null) },
            title = { Text("删除 DNS 记录") },
            text = { Text("确定删除 ${record.type} 记录「${record.name} → ${record.content}」吗？") },
            confirmButton = {
                TextButton(onClick = viewModel::deleteRecord, enabled = !state.actionInProgress) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteRecord(null) }) { Text("取消") }
            },
        )
    }
}

// --- 隧道 ---

@Composable
private fun TunnelsTab(state: CloudflareUiState, viewModel: CloudflareViewModel) {
    Box(modifier = Modifier.fillMaxSize()) {
        if (state.tunnelsLoading && state.tunnels.isEmpty()) {
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
                item {
                    NpButton(
                        text = "创建隧道",
                        onClick = { viewModel.showCreateTunnel(true) },
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                if (state.tunnels.isEmpty()) {
                    item {
                        Text(
                            "暂无隧道",
                            style = MaterialTheme.typography.bodyMedium,
                            color = NpTheme.colors.textTertiary,
                        )
                    }
                }
                items(state.tunnels, key = { it.id }) { tunnel ->
                    TunnelCard(
                        tunnel = tunnel,
                        busy = state.actionInProgress,
                        onCtl = { start -> viewModel.tunnelCtl(tunnel, start) },
                        onRename = { viewModel.showRenameTunnel(tunnel) },
                        onDelete = { viewModel.showDeleteTunnel(tunnel) },
                    )
                }
            }
        }
    }
}

@Composable
private fun TunnelCard(
    tunnel: TunnelView,
    busy: Boolean,
    onCtl: (start: Boolean) -> Unit,
    onRename: () -> Unit,
    onDelete: () -> Unit,
) {
    val running = tunnel.process == "active" ||
        (tunnel.process.isBlank() && tunnel.status == "healthy")

    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        tunnel.name,
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        tunnel.node?.let { "节点 ${it.name}" } ?: "未关联节点",
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                    )
                }
                StatusBadge(
                    text = tunnel.status.ifBlank { "unknown" },
                    kind = if (tunnel.status == "healthy") NpBadgeKind.Success else NpBadgeKind.Warning,
                )
            }
            Spacer(Modifier.height(6.dp))
            Text(
                buildList {
                    if (tunnel.process.isNotBlank()) add("进程 ${tunnel.process}")
                    if (tunnel.version.isNotBlank()) add("cloudflared ${tunnel.version}")
                    if (tunnel.managed) add("面板托管")
                    if (!tunnel.online && tunnel.node != null) add("节点离线")
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
            Row {
                if (tunnel.node != null) {
                    if (running) {
                        NpGhostButton(text = "停止", onClick = { onCtl(false) }, enabled = !busy && tunnel.online, danger = true)
                    } else {
                        NpGhostButton(text = "启动", onClick = { onCtl(true) }, enabled = !busy && tunnel.online)
                    }
                }
                NpGhostButton(text = "重命名", onClick = onRename, enabled = !busy)
                NpGhostButton(text = "删除", onClick = onDelete, enabled = !busy, danger = true)
            }
        }
    }
}

// --- 域名 ---

@Composable
private fun DomainsTab(state: CloudflareUiState, viewModel: CloudflareViewModel) {
    Box(modifier = Modifier.fillMaxSize()) {
        if (state.domainsLoading && state.domainTunnels.isEmpty()) {
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
                if (state.domainTunnels.isEmpty()) {
                    item {
                        Text(
                            "暂无隧道",
                            style = MaterialTheme.typography.bodyMedium,
                            color = NpTheme.colors.textTertiary,
                        )
                    }
                }
                items(state.domainTunnels, key = { it.id }) { tunnel ->
                    DomainTunnelCard(tunnel, state.actionInProgress, viewModel)
                }
            }
        }
    }
}

@Composable
private fun DomainTunnelCard(
    tunnel: DomainTunnelView,
    busy: Boolean,
    viewModel: CloudflareViewModel,
) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        tunnel.name,
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    tunnel.node?.let {
                        Text(
                            "节点 ${it.name}",
                            style = MaterialTheme.typography.bodySmall,
                            color = NpTheme.colors.textSecondary,
                        )
                    }
                }
                NpGhostButton(text = "加规则", onClick = { viewModel.openRuleForm(tunnel, null) }, enabled = !busy)
            }
            if (tunnel.error.isNotBlank()) {
                Text(
                    "读取配置失败：${tunnel.error}",
                    style = MaterialTheme.typography.bodySmall,
                    color = NpTheme.colors.warning,
                )
            }
            tunnel.rules.forEach { rule ->
                Spacer(Modifier.height(6.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(modifier = Modifier.weight(1f)) {
                        if (rule.isCatchAll) {
                            Text(
                                "(catch-all) → ${rule.service}",
                                style = MaterialTheme.typography.bodyMedium,
                                color = NpTheme.colors.textSecondary,
                            )
                        } else {
                            Text(
                                rule.hostname + rule.path,
                                style = MaterialTheme.typography.bodyMedium,
                                color = NpTheme.colors.textPrimary,
                            )
                            Text(
                                buildList {
                                    add("→ ${rule.service}")
                                    rule.dns?.let { dns ->
                                        add(
                                            when {
                                                dns.target.isBlank() -> "CNAME 缺失"
                                                dns.matches -> "DNS ✓"
                                                else -> "DNS 指向 ${dns.target}"
                                            },
                                        )
                                    }
                                }.joinToString("  "),
                                style = MaterialTheme.typography.labelSmall,
                                color = when {
                                    rule.dns == null -> NpTheme.colors.textTertiary
                                    rule.dns.matches -> NpTheme.colors.success
                                    else -> NpTheme.colors.warning
                                },
                            )
                        }
                    }
                    if (!rule.isCatchAll) {
                        NpGhostButton(text = "编辑", onClick = { viewModel.openRuleForm(tunnel, rule) }, enabled = !busy)
                        NpGhostButton(text = "换隧道", onClick = { viewModel.openMoveForm(tunnel, rule) }, enabled = !busy)
                        NpGhostButton(
                            text = "删除",
                            onClick = { viewModel.showDeleteRule(tunnel to rule) },
                            enabled = !busy,
                            danger = true,
                        )
                    }
                }
            }
        }
    }
}

// --- DNS ---

@Composable
private fun DnsTab(state: CloudflareUiState, viewModel: CloudflareViewModel) {
    Column(modifier = Modifier.fillMaxSize()) {
        Column(modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
            ZoneDropdown(
                zones = state.zones,
                selectedId = state.selectedZoneId,
                onSelect = viewModel::selectZone,
            )
            Spacer(Modifier.height(8.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                OutlinedTextField(
                    value = state.recordFilter,
                    onValueChange = viewModel::setRecordFilter,
                    label = { Text("搜索（名称/内容/类型）") },
                    singleLine = true,
                    modifier = Modifier.weight(1f),
                )
                Spacer(Modifier.width(8.dp))
                NpButton(
                    text = "新建",
                    onClick = { viewModel.openRecordForm(null) },
                    enabled = state.selectedZoneId.isNotBlank(),
                )
            }
        }
        Box(modifier = Modifier.fillMaxSize()) {
            when {
                state.zonesLoading -> LinearProgressIndicator(
                    modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                    color = NpTheme.colors.primary,
                    trackColor = NpTheme.colors.bgTertiary,
                )
                state.recordsLoading && state.records.isEmpty() -> LinearProgressIndicator(
                    modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                    color = NpTheme.colors.primary,
                    trackColor = NpTheme.colors.bgTertiary,
                )
                else -> {
                    val records = viewModel.filteredRecords()
                    LazyColumn(
                        modifier = Modifier.fillMaxSize(),
                        contentPadding = PaddingValues(16.dp),
                        verticalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        if (records.isEmpty()) {
                            item {
                                Text(
                                    "无记录",
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = NpTheme.colors.textTertiary,
                                )
                            }
                        }
                        items(records, key = { it.id }) { record ->
                            DnsRecordCard(
                                record = record,
                                busy = state.actionInProgress,
                                onEdit = { viewModel.openRecordForm(record) },
                                onDelete = { viewModel.showDeleteRecord(record) },
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun DnsRecordCard(
    record: DnsRecord,
    busy: Boolean,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                StatusBadge(text = record.type, kind = NpBadgeKind.Muted)
                Spacer(Modifier.width(8.dp))
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        record.name,
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                        maxLines = 1,
                    )
                    Text(
                        record.content,
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                        maxLines = 1,
                    )
                }
                if (record.proxied) {
                    StatusBadge(text = "代理", kind = NpBadgeKind.Amber)
                }
            }
            Text(
                buildList {
                    add(if (record.ttl == 1) "TTL Auto" else "TTL ${record.ttl}")
                    record.priority?.let { add("优先级 $it") }
                    if (record.comment.isNotBlank()) add(record.comment)
                }.joinToString(" · "),
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
            Row {
                NpGhostButton(text = "编辑", onClick = onEdit, enabled = !busy)
                NpGhostButton(text = "删除", onClick = onDelete, enabled = !busy, danger = true)
            }
        }
    }
}

// --- dialogs ---

@Composable
private fun CreateTunnelDialog(
    nodes: List<NodeView>,
    busy: Boolean,
    onConfirm: (nodeId: String, name: String) -> Unit,
    onDismiss: () -> Unit,
) {
    var nodeId by remember { mutableStateOf("") }
    var name by remember { mutableStateOf("") }
    var open by remember { mutableStateOf(false) }
    val online = nodes.filter { it.online }
    val selected = online.firstOrNull { it.id == nodeId }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("创建隧道") },
        text = {
            Column {
                Box {
                    NpOutlineButton(
                        text = selected?.name?.ifBlank { selected.id.take(8) } ?: "选择节点（在线）",
                        onClick = { open = true },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
                        online.forEach { node ->
                            DropdownMenuItem(
                                text = { Text(node.name.ifBlank { node.hostname.ifBlank { node.id.take(8) } }) },
                                onClick = { open = false; nodeId = node.id },
                            )
                        }
                    }
                }
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("隧道名称") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    "每节点限一个面板隧道；创建约需 1-3 分钟（节点安装 cloudflared）。",
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onConfirm(nodeId, name) },
                enabled = !busy && nodeId.isNotBlank() && name.isNotBlank(),
            ) { Text("创建") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun RuleFormDialog(
    form: RuleForm,
    busy: Boolean,
    onSave: (hostname: String, path: String, service: String) -> Unit,
    onDismiss: () -> Unit,
) {
    var hostname by remember { mutableStateOf(form.editing?.hostname ?: "") }
    var path by remember { mutableStateOf(form.editing?.path ?: "") }
    var service by remember { mutableStateOf(form.editing?.service ?: "") }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (form.editing == null) "新增域名规则" else "编辑域名规则") },
        text = {
            Column {
                Text(
                    "隧道：${form.tunnel.name}",
                    style = MaterialTheme.typography.bodySmall,
                    color = NpTheme.colors.textTertiary,
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = hostname,
                    onValueChange = { hostname = it },
                    label = { Text("域名（如 app.example.com）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = path,
                    onValueChange = { path = it },
                    label = { Text("路径（可选，如 api/*）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = service,
                    onValueChange = { service = it },
                    label = { Text("指向 service（如 http://localhost:8080）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onSave(hostname, path, service) },
                enabled = !busy && hostname.isNotBlank() && service.isNotBlank(),
            ) { Text("保存") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun MoveDialog(
    form: MoveForm,
    tunnels: List<DomainTunnelView>,
    busy: Boolean,
    onConfirm: (toTunnelId: String) -> Unit,
    onDismiss: () -> Unit,
) {
    var toId by remember { mutableStateOf("") }
    var open by remember { mutableStateOf(false) }
    val candidates = tunnels.filter { it.id != form.fromTunnel.id }
    val selected = candidates.firstOrNull { it.id == toId }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("换隧道") },
        text = {
            Column {
                Text(
                    "将「${form.hostname}」从「${form.fromTunnel.name}」移到目标隧道：先加目标规则 → 切换 DNS → 清理源规则（失败自动回滚）。",
                    style = MaterialTheme.typography.bodySmall,
                )
                Spacer(Modifier.height(8.dp))
                Box {
                    NpOutlineButton(
                        text = selected?.name ?: "选择目标隧道",
                        onClick = { open = true },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
                        candidates.forEach { t ->
                            DropdownMenuItem(text = { Text(t.name) }, onClick = { open = false; toId = t.id })
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { onConfirm(toId) }, enabled = !busy && toId.isNotBlank()) {
                Text("移动", color = NpTheme.colors.warning)
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun RecordFormDialog(
    form: RecordForm,
    busy: Boolean,
    onSave: (DnsRecordRequest) -> Unit,
    onDismiss: () -> Unit,
) {
    val types = listOf("A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA")
    var type by remember { mutableStateOf(form.editing?.type ?: "A") }
    var name by remember { mutableStateOf(form.editing?.name ?: "") }
    var content by remember { mutableStateOf(form.editing?.content ?: "") }
    var ttl by remember { mutableStateOf(form.editing?.ttl?.toString() ?: "1") }
    var proxied by remember { mutableStateOf(form.editing?.proxied ?: false) }
    var priority by remember { mutableStateOf(form.editing?.priority?.toString() ?: "") }
    var comment by remember { mutableStateOf(form.editing?.comment ?: "") }
    var typeOpen by remember { mutableStateOf(false) }

    val proxiable = type in listOf("A", "AAAA", "CNAME")
    val usesPriority = type in listOf("MX", "SRV")

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (form.editing == null) "新建 DNS 记录" else "编辑 DNS 记录") },
        text = {
            Column(modifier = Modifier.verticalScroll(rememberScrollState())) {
                Box {
                    NpOutlineButton(
                        text = "类型：$type",
                        onClick = { typeOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    DropdownMenu(expanded = typeOpen, onDismissRequest = { typeOpen = false }) {
                        types.forEach { t ->
                            DropdownMenuItem(text = { Text(t) }, onClick = { typeOpen = false; type = t })
                        }
                    }
                }
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("名称（如 www 或 @）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = content,
                    onValueChange = { content = it },
                    label = {
                        Text(
                            when (type) {
                                "A" -> "内容（IPv4，如 1.2.3.4）"
                                "AAAA" -> "内容（IPv6）"
                                "CNAME" -> "内容（目标域名）"
                                "MX" -> "内容（邮件服务器域名）"
                                else -> "内容"
                            },
                        )
                    },
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                Row {
                    OutlinedTextField(
                        value = ttl,
                        onValueChange = { ttl = it.filter(Char::isDigit) },
                        label = { Text("TTL（1=Auto）") },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    if (usesPriority) {
                        Spacer(Modifier.width(8.dp))
                        OutlinedTextField(
                            value = priority,
                            onValueChange = { priority = it.filter(Char::isDigit) },
                            label = { Text("优先级") },
                            singleLine = true,
                            modifier = Modifier.weight(1f),
                        )
                    }
                }
                if (proxiable) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("CF 代理（橙色云）", modifier = Modifier.weight(1f))
                        Switch(checked = proxied, onCheckedChange = { proxied = it })
                    }
                }
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = comment,
                    onValueChange = { comment = it },
                    label = { Text("备注（可选）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    onSave(
                        DnsRecordRequest(
                            type = type,
                            name = name.trim(),
                            content = content.trim(),
                            ttl = ttl.toIntOrNull() ?: 1,
                            proxied = proxied && proxiable,
                            priority = if (usesPriority) priority.toIntOrNull() else null,
                            comment = comment.trim(),
                        ),
                    )
                },
                enabled = !busy && name.isNotBlank() && content.isNotBlank(),
            ) { Text("保存") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

@Composable
private fun ZoneDropdown(
    zones: List<DnsZone>,
    selectedId: String,
    onSelect: (String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    val selected = zones.firstOrNull { it.id == selectedId }
    Box {
        NpOutlineButton(
            text = selected?.name ?: "选择 Zone",
            onClick = { open = true },
            modifier = Modifier.fillMaxWidth(),
        )
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            zones.forEach { z ->
                DropdownMenuItem(text = { Text(z.name) }, onClick = { open = false; onSelect(z.id) })
            }
        }
    }
}
