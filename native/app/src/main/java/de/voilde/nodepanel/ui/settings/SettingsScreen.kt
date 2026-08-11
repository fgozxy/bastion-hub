package de.voilde.nodepanel.ui.settings

import androidx.compose.foundation.clickable
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
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.Notifications
import androidx.compose.material.icons.filled.Person
import androidx.compose.material.icons.filled.Save
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.FilterChip
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
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.TargetView
import de.voilde.nodepanel.ui.components.NpBadgeKind
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.components.NpCard
import de.voilde.nodepanel.ui.components.NpDivider
import de.voilde.nodepanel.ui.components.NpGhostButton
import de.voilde.nodepanel.ui.components.NpOutlineButton
import de.voilde.nodepanel.ui.components.NpTopBar
import de.voilde.nodepanel.ui.components.StatusBadge
import de.voilde.nodepanel.ui.theme.NpTheme

@Composable
fun SettingsScreen(
    container: AppContainer,
    onBack: () -> Unit,
    onLogout: () -> Unit,
    onCheckUpdate: () -> Unit = {},
    currentVersion: String = "",
    viewModel: SettingsViewModel = viewModel { SettingsViewModel(container) },
) {
    val state by viewModel.uiState.collectAsState()
    val snackbarHostState = remember { SnackbarHostState() }
    var section by rememberSaveable { mutableStateOf<String?>(null) }

    LaunchedEffect(state.message) {
        state.message?.let {
            snackbarHostState.showSnackbar(it)
            viewModel.clearMessage()
        }
    }

    val title = when (section) {
        "account" -> "账户"
        "telegram" -> "通知 Telegram"
        "backup" -> "备份"
        "cloudflare" -> "Cloudflare"
        "komari" -> "集成 Komari"
        "targets" -> "备份目标"
        "about" -> "关于"
        else -> "设置"
    }

    Scaffold(
        containerColor = NpTheme.colors.bgPage,
        topBar = {
            NpTopBar(
                title = title,
                showBrand = false,
                navigationIcon = {
                    IconButton(onClick = { if (section == null) onBack() else section = null }) {
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
                state.loading -> LinearProgressIndicator(
                    modifier = Modifier.fillMaxWidth().align(Alignment.TopCenter),
                    color = NpTheme.colors.primary,
                    trackColor = NpTheme.colors.bgTertiary,
                )
                state.error != null -> Column(
                    modifier = Modifier.align(Alignment.Center),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Text(state.error!!, color = NpTheme.colors.warning)
                    TextButton(onClick = { viewModel.load() }) { Text("重试") }
                }
                section == null -> SectionList(state, onSelect = { section = it })
                else -> when (section) {
                    "account" -> AccountSection(state, viewModel, onLogout)
                    "telegram" -> TelegramSection(state, viewModel)
                    "backup" -> BackupSection(state, viewModel)
                    "cloudflare" -> CloudflareSection(state, viewModel)
                    "komari" -> KomariSection(state, viewModel)
                    "targets" -> TargetsSection(state, viewModel)
                    "about" -> AboutSection(state, container, currentVersion, onCheckUpdate)
                }
            }
        }
    }

    state.targetForm?.let { form ->
        TargetFormDialog(
            initial = form,
            busy = state.busy,
            onSave = viewModel::saveTarget,
            onDismiss = viewModel::closeTargetForm,
        )
    }

    state.deleteTargetTarget?.let { t ->
        AlertDialog(
            onDismissRequest = { viewModel.showDeleteTarget(null) },
            title = { Text("删除备份目标") },
            text = { Text("确定删除目标「${t.name}」吗？已推送到该目标的存档不会被删除，但后续备份不会再推送。") },
            confirmButton = {
                TextButton(onClick = viewModel::deleteTarget, enabled = !state.busy) {
                    Text("删除", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { viewModel.showDeleteTarget(null) }) { Text("取消") }
            },
        )
    }
}

// --- 分组列表 ---

private data class SectionItem(
    val key: String,
    val icon: ImageVector,
    val title: String,
    val subtitle: String,
)

@Composable
private fun SectionList(state: SettingsUiState, onSelect: (String) -> Unit) {
    val items = listOf(
        SectionItem("account", Icons.Filled.Person, "账户", state.username.ifBlank { "用户名与密码" }),
        SectionItem(
            "telegram", Icons.Filled.Notifications, "通知 Telegram",
            if (state.tgToken.isNotBlank()) "已配置" else "未配置",
        ),
        SectionItem("backup", Icons.Filled.Save, "备份", "保留策略 · 排除路径 · 容器监控"),
        SectionItem(
            "cloudflare", Icons.Filled.Cloud, "Cloudflare",
            if (state.cfToken.isNotBlank()) "令牌已配置" else "API 令牌 · 公网地址",
        ),
        SectionItem(
            "komari", Icons.Filled.Link, "集成 Komari",
            state.komariBase.ifBlank { "探针服务" },
        ),
        SectionItem("targets", Icons.Filled.Storage, "备份目标", "${state.targets.size} 个目标"),
        SectionItem("about", Icons.Filled.Info, "关于", "版本 · 服务器 · 检查更新"),
    )
    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
    ) {
        item {
            NpCard(modifier = Modifier.fillMaxWidth()) {
                Column {
                    items.forEachIndexed { i, item ->
                        if (i > 0) NpDivider()
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier
                                .fillMaxWidth()
                                .clickable { onSelect(item.key) }
                                .padding(horizontal = 12.dp, vertical = 12.dp),
                        ) {
                            Icon(
                                item.icon,
                                contentDescription = null,
                                tint = NpTheme.colors.textSecondary,
                                modifier = Modifier.width(20.dp),
                            )
                            Spacer(Modifier.width(12.dp))
                            Column(modifier = Modifier.weight(1f)) {
                                Text(
                                    item.title,
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = NpTheme.colors.textPrimary,
                                )
                                Text(
                                    item.subtitle,
                                    style = MaterialTheme.typography.labelSmall,
                                    color = NpTheme.colors.textTertiary,
                                )
                            }
                            Icon(
                                Icons.AutoMirrored.Filled.KeyboardArrowRight,
                                contentDescription = null,
                                tint = NpTheme.colors.textTertiary,
                            )
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun FormColumn(content: @Composable () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        content()
    }
}

// --- 账户 ---

@Composable
private fun AccountSection(state: SettingsUiState, viewModel: SettingsViewModel, onLogout: () -> Unit) {
    var username by remember(state.username) { mutableStateOf(state.username) }
    var newPassword by remember { mutableStateOf("") }
    var logoutConfirm by remember { mutableStateOf(false) }

    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                OutlinedTextField(
                    value = username,
                    onValueChange = { username = it },
                    label = { Text("用户名") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = newPassword,
                    onValueChange = { newPassword = it },
                    label = { Text("新密码（留空则不修改）") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                NpButton(
                    text = "保存",
                    onClick = { viewModel.saveAccount(username.trim(), newPassword) },
                    enabled = !state.busy && username.isNotBlank(),
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.padding(12.dp),
            ) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        "退出登录",
                        style = MaterialTheme.typography.bodyMedium,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        "清除本机会话，返回登录页",
                        style = MaterialTheme.typography.labelSmall,
                        color = NpTheme.colors.textTertiary,
                    )
                }
                NpGhostButton(
                    text = "退出",
                    onClick = { logoutConfirm = true },
                    danger = true,
                )
            }
        }
    }

    if (logoutConfirm) {
        AlertDialog(
            onDismissRequest = { logoutConfirm = false },
            title = { Text("退出登录") },
            text = { Text("确定退出当前账户吗？") },
            confirmButton = {
                TextButton(onClick = { viewModel.logout(onLogout) }) {
                    Text("退出", color = NpTheme.colors.warning)
                }
            },
            dismissButton = {
                TextButton(onClick = { logoutConfirm = false }) { Text("取消") }
            },
        )
    }
}

// --- 通知 Telegram ---

@Composable
private fun TelegramSection(state: SettingsUiState, viewModel: SettingsViewModel) {
    var token by remember(state.tgToken) { mutableStateOf(state.tgToken) }
    var chatId by remember(state.tgChatId) { mutableStateOf(state.tgChatId) }

    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                OutlinedTextField(
                    value = token,
                    onValueChange = { token = it },
                    label = { Text("Bot Token") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = chatId,
                    onValueChange = { chatId = it },
                    label = { Text("Chat ID") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                Row {
                    NpOutlineButton(
                        text = if (state.testing) "测试中…" else "发送测试",
                        onClick = { viewModel.testTelegram(token, chatId) },
                        enabled = !state.testing && token.isNotBlank() && chatId.isNotBlank(),
                        modifier = Modifier.weight(1f),
                    )
                    Spacer(Modifier.width(8.dp))
                    NpButton(
                        text = "保存",
                        onClick = { viewModel.saveTelegram(token, chatId) },
                        enabled = !state.busy,
                        modifier = Modifier.weight(1f),
                    )
                }
            }
        }
    }
}

// --- 备份 ---

@Composable
private fun BackupSection(state: SettingsUiState, viewModel: SettingsViewModel) {
    var keepCount by remember(state.keepCount) { mutableStateOf(state.keepCount) }
    var keepDays by remember(state.keepDays) { mutableStateOf(state.keepDays) }
    var excludes by remember(state.excludes) { mutableStateOf(state.excludes) }
    var monitorEnabled by remember(state.monitorEnabled) { mutableStateOf(state.monitorEnabled) }
    var monitorInterval by remember(state.monitorInterval) { mutableStateOf(state.monitorInterval) }

    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Text(
                    "保留策略",
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                )
                Spacer(Modifier.height(8.dp))
                Row {
                    OutlinedTextField(
                        value = keepCount,
                        onValueChange = { keepCount = it.filter(Char::isDigit) },
                        label = { Text("保留份数") },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    Spacer(Modifier.width(8.dp))
                    OutlinedTextField(
                        value = keepDays,
                        onValueChange = { keepDays = it.filter(Char::isDigit) },
                        label = { Text("保留天数") },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                }
                Spacer(Modifier.height(12.dp))
                NpButton(
                    text = "保存保留策略",
                    onClick = { viewModel.saveRetention(keepCount, keepDays) },
                    enabled = !state.busy,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Text(
                    "排除路径",
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                )
                Text(
                    "备份打包时跳过的主机路径前缀，每行一个",
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = excludes,
                    onValueChange = { excludes = it },
                    minLines = 3,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                NpButton(
                    text = "保存排除路径",
                    onClick = { viewModel.saveExcludes(excludes) },
                    enabled = !state.busy,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(modifier = Modifier.weight(1f)) {
                        Text(
                            "容器异常监控",
                            style = MaterialTheme.typography.titleSmall,
                            color = NpTheme.colors.textPrimary,
                        )
                        Text(
                            "周期性扫描容器健康状态",
                            style = MaterialTheme.typography.labelSmall,
                            color = NpTheme.colors.textTertiary,
                        )
                    }
                    Switch(checked = monitorEnabled, onCheckedChange = { monitorEnabled = it })
                }
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = monitorInterval,
                    onValueChange = { monitorInterval = it.filter(Char::isDigit) },
                    label = { Text("扫描间隔（秒，最低 30）") },
                    singleLine = true,
                    enabled = monitorEnabled,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                NpButton(
                    text = "保存监控配置",
                    onClick = { viewModel.saveMonitor(monitorEnabled, monitorInterval) },
                    enabled = !state.busy,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
    }
}

// --- Cloudflare ---

@Composable
private fun CloudflareSection(state: SettingsUiState, viewModel: SettingsViewModel) {
    var token by remember(state.cfToken) { mutableStateOf(state.cfToken) }
    var publicUrl by remember(state.publicUrl) { mutableStateOf(state.publicUrl) }

    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Text(
                    "API 令牌",
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                )
                Text(
                    "用于容器迁移时改写隧道 ingress 与 DNS（需 Tunnel:Edit + DNS:Edit）",
                    style = MaterialTheme.typography.labelSmall,
                    color = NpTheme.colors.textTertiary,
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = token,
                    onValueChange = { token = it },
                    label = { Text("API Token（留空 = 清除）") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                Row {
                    NpOutlineButton(
                        text = if (state.testing) "测试中…" else "测试令牌",
                        onClick = { viewModel.testCloudflare(token) },
                        enabled = !state.testing,
                        modifier = Modifier.weight(1f),
                    )
                    Spacer(Modifier.width(8.dp))
                    NpButton(
                        text = "保存",
                        onClick = { viewModel.saveCloudflare(token) },
                        enabled = !state.busy,
                        modifier = Modifier.weight(1f),
                    )
                }
            }
        }
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                Text(
                    "面板公网地址",
                    style = MaterialTheme.typography.titleSmall,
                    color = NpTheme.colors.textPrimary,
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = publicUrl,
                    onValueChange = { publicUrl = it },
                    label = { Text("https://panel.example.com") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                NpButton(
                    text = "保存公网地址",
                    onClick = { viewModel.saveDomain(publicUrl) },
                    enabled = !state.busy,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
    }
}

// --- 集成 Komari ---

@Composable
private fun KomariSection(state: SettingsUiState, viewModel: SettingsViewModel) {
    var base by remember(state.komariBase) { mutableStateOf(state.komariBase) }
    var key by remember(state.komariKey) { mutableStateOf(state.komariKey) }
    var install by remember(state.komariInstall) { mutableStateOf(state.komariInstall) }

    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                OutlinedTextField(
                    value = base,
                    onValueChange = { base = it },
                    label = { Text("Komari 地址（如 https://komari.example.com）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = key,
                    onValueChange = { key = it },
                    label = { Text("API Key") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = install,
                    onValueChange = { install = it },
                    label = { Text("安装脚本地址（留空 = 官方脚本）") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(12.dp))
                Row {
                    NpOutlineButton(
                        text = if (state.testing) "测试中…" else "测试连接",
                        onClick = { viewModel.testKomari(base, key) },
                        enabled = !state.testing,
                        modifier = Modifier.weight(1f),
                    )
                    Spacer(Modifier.width(8.dp))
                    NpButton(
                        text = "保存",
                        onClick = { viewModel.saveKomari(base, key, install) },
                        enabled = !state.busy,
                        modifier = Modifier.weight(1f),
                    )
                }
            }
        }
    }
}

// --- 备份目标 ---

@Composable
private fun TargetsSection(state: SettingsUiState, viewModel: SettingsViewModel) {
    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item {
            NpButton(
                text = "新建目标",
                onClick = { viewModel.openTargetForm(null) },
                enabled = !state.busy,
                modifier = Modifier.fillMaxWidth(),
            )
        }
        if (state.targets.isEmpty()) {
            item {
                Text(
                    "暂无备份目标",
                    style = MaterialTheme.typography.bodyMedium,
                    color = NpTheme.colors.textTertiary,
                )
            }
        }
        items(state.targets, key = { it.id }) { t ->
            TargetCard(t, state, viewModel)
        }
        item {
            Text(
                "OneDrive 目标需在网页端通过设备码授权创建；此处可测试/启停/删除。",
                style = MaterialTheme.typography.labelSmall,
                color = NpTheme.colors.textTertiary,
            )
        }
    }
}

@Composable
private fun TargetCard(t: TargetView, state: SettingsUiState, viewModel: SettingsViewModel) {
    NpCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        t.name.ifBlank { t.id.take(8) },
                        style = MaterialTheme.typography.titleSmall,
                        color = NpTheme.colors.textPrimary,
                    )
                    Text(
                        targetSubtitle(t),
                        style = MaterialTheme.typography.labelSmall,
                        color = NpTheme.colors.textTertiary,
                        maxLines = 1,
                    )
                }
                StatusBadge(
                    text = t.type,
                    kind = if (t.enabled) NpBadgeKind.Success else NpBadgeKind.Muted,
                )
                Spacer(Modifier.width(8.dp))
                Switch(
                    checked = t.enabled,
                    onCheckedChange = { viewModel.toggleTarget(t) },
                    enabled = !state.busy,
                )
            }
            Row {
                NpGhostButton(
                    text = if (state.testing) "测试中…" else "测试",
                    onClick = { viewModel.testTarget(t.id) },
                    enabled = !state.testing,
                )
                if (t.type != "onedrive") {
                    NpGhostButton(text = "编辑", onClick = { viewModel.openTargetForm(t) }, enabled = !state.busy)
                }
                NpGhostButton(
                    text = "删除",
                    onClick = { viewModel.showDeleteTarget(t) },
                    enabled = !state.busy,
                    danger = true,
                )
            }
        }
    }
}

private fun targetSubtitle(t: TargetView): String = when (t.type) {
    "github" -> "GitHub 仓库"
    "s3" -> "S3 兼容存储"
    "vps" -> "SFTP 远程主机"
    "onedrive" -> "OneDrive"
    else -> t.type
}

@Composable
private fun TargetFormDialog(
    initial: TargetForm,
    busy: Boolean,
    onSave: (TargetForm) -> Unit,
    onDismiss: () -> Unit,
) {
    var type by remember { mutableStateOf(initial.type) }
    var name by remember { mutableStateOf(initial.name) }
    var ghToken by remember { mutableStateOf(initial.ghToken) }
    var ghRepo by remember { mutableStateOf(initial.ghRepo) }
    var s3Endpoint by remember { mutableStateOf(initial.s3Endpoint) }
    var s3Access by remember { mutableStateOf(initial.s3Access) }
    var s3Secret by remember { mutableStateOf(initial.s3Secret) }
    var s3Bucket by remember { mutableStateOf(initial.s3Bucket) }
    var s3Prefix by remember { mutableStateOf(initial.s3Prefix) }
    var s3Region by remember { mutableStateOf(initial.s3Region) }
    var s3PathStyle by remember { mutableStateOf(initial.s3PathStyle) }
    var s3Secure by remember { mutableStateOf(initial.s3Secure) }
    var vpsHost by remember { mutableStateOf(initial.vpsHost) }
    var vpsPort by remember { mutableStateOf(initial.vpsPort) }
    var vpsUser by remember { mutableStateOf(initial.vpsUser) }
    var vpsPassword by remember { mutableStateOf(initial.vpsPassword) }
    var vpsKeyPem by remember { mutableStateOf(initial.vpsKeyPem) }
    var vpsBaseDir by remember { mutableStateOf(initial.vpsBaseDir) }

    val creating = initial.editing == null
    val valid = name.isNotBlank() && when (type) {
        "github" -> ghToken.isNotBlank()
        "s3" -> s3Endpoint.isNotBlank() && s3Access.isNotBlank() && s3Secret.isNotBlank() && s3Bucket.isNotBlank()
        "vps" -> vpsHost.isNotBlank() && vpsUser.isNotBlank() && (vpsPassword.isNotBlank() || vpsKeyPem.isNotBlank())
        else -> false
    }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(if (creating) "新建备份目标" else "编辑备份目标") },
        text = {
            Column(modifier = Modifier.verticalScroll(rememberScrollState())) {
                if (creating) {
                    Row {
                        FilterChip(
                            selected = type == "github",
                            onClick = { type = "github" },
                            label = { Text("GitHub") },
                        )
                        Spacer(Modifier.width(6.dp))
                        FilterChip(
                            selected = type == "s3",
                            onClick = { type = "s3" },
                            label = { Text("S3") },
                        )
                        Spacer(Modifier.width(6.dp))
                        FilterChip(
                            selected = type == "vps",
                            onClick = { type = "vps" },
                            label = { Text("SFTP") },
                        )
                    }
                    Spacer(Modifier.height(8.dp))
                }
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("名称") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                when (type) {
                    "github" -> {
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = ghToken,
                            onValueChange = { ghToken = it },
                            label = { Text("Personal Access Token") },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = ghRepo,
                            onValueChange = { ghRepo = it },
                            label = { Text("仓库名（留空 = nodepanel-backups）") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(4.dp))
                        Text(
                            "服务器会自动识别账号并确保私有仓库存在。",
                            style = MaterialTheme.typography.labelSmall,
                            color = NpTheme.colors.textTertiary,
                        )
                    }
                    "s3" -> {
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = s3Endpoint,
                            onValueChange = { s3Endpoint = it },
                            label = { Text("Endpoint（如 minio.example.com）") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = s3Access,
                            onValueChange = { s3Access = it },
                            label = { Text("Access Key") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = s3Secret,
                            onValueChange = { s3Secret = it },
                            label = { Text("Secret Key") },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        Row {
                            OutlinedTextField(
                                value = s3Bucket,
                                onValueChange = { s3Bucket = it },
                                label = { Text("Bucket") },
                                singleLine = true,
                                modifier = Modifier.weight(1f),
                            )
                            Spacer(Modifier.width(8.dp))
                            OutlinedTextField(
                                value = s3Prefix,
                                onValueChange = { s3Prefix = it },
                                label = { Text("前缀（可选）") },
                                singleLine = true,
                                modifier = Modifier.weight(1f),
                            )
                        }
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = s3Region,
                            onValueChange = { s3Region = it },
                            label = { Text("Region（可选，MinIO 忽略）") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text("Path Style（MinIO/自建）", modifier = Modifier.weight(1f))
                            Switch(checked = s3PathStyle, onCheckedChange = { s3PathStyle = it })
                        }
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text("TLS（Endpoint 无协议头时）", modifier = Modifier.weight(1f))
                            Switch(checked = s3Secure, onCheckedChange = { s3Secure = it })
                        }
                    }
                    "vps" -> {
                        Spacer(Modifier.height(8.dp))
                        Row {
                            OutlinedTextField(
                                value = vpsHost,
                                onValueChange = { vpsHost = it },
                                label = { Text("主机") },
                                singleLine = true,
                                modifier = Modifier.weight(1f),
                            )
                            Spacer(Modifier.width(8.dp))
                            OutlinedTextField(
                                value = vpsPort,
                                onValueChange = { vpsPort = it.filter(Char::isDigit) },
                                label = { Text("端口") },
                                singleLine = true,
                                modifier = Modifier.width(88.dp),
                            )
                        }
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = vpsUser,
                            onValueChange = { vpsUser = it },
                            label = { Text("用户") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = vpsPassword,
                            onValueChange = { vpsPassword = it },
                            label = { Text("密码（或下方私钥）") },
                            singleLine = true,
                            visualTransformation = PasswordVisualTransformation(),
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = vpsKeyPem,
                            onValueChange = { vpsKeyPem = it },
                            label = { Text("私钥 PEM（可选）") },
                            minLines = 2,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(8.dp))
                        OutlinedTextField(
                            value = vpsBaseDir,
                            onValueChange = { vpsBaseDir = it },
                            label = { Text("存储目录（如 /backups）") },
                            singleLine = true,
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    onSave(
                        initial.copy(
                            type = type, name = name,
                            ghToken = ghToken, ghRepo = ghRepo,
                            s3Endpoint = s3Endpoint, s3Access = s3Access, s3Secret = s3Secret,
                            s3Bucket = s3Bucket, s3Prefix = s3Prefix, s3Region = s3Region,
                            s3PathStyle = s3PathStyle, s3Secure = s3Secure,
                            vpsHost = vpsHost, vpsPort = vpsPort, vpsUser = vpsUser,
                            vpsPassword = vpsPassword, vpsKeyPem = vpsKeyPem, vpsBaseDir = vpsBaseDir,
                        ),
                    )
                },
                enabled = !busy && valid,
            ) { Text("保存") }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text("取消") }
        },
    )
}

// --- 关于 ---

@Composable
private fun AboutSection(
    state: SettingsUiState,
    container: AppContainer,
    currentVersion: String,
    onCheckUpdate: () -> Unit,
) {
    FormColumn {
        NpCard(modifier = Modifier.fillMaxWidth()) {
            Column(modifier = Modifier.padding(12.dp)) {
                AboutRow("应用版本", if (currentVersion.isNotBlank()) "v$currentVersion" else "-")
                Spacer(Modifier.height(8.dp))
                AboutRow("服务器", container.sessionManager.baseUrl)
                Spacer(Modifier.height(8.dp))
                AboutRow("登录账户", state.username.ifBlank { "-" })
            }
        }
        NpButton(
            text = "检查更新",
            onClick = onCheckUpdate,
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun AboutRow(label: String, value: String) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            label,
            style = MaterialTheme.typography.bodyMedium,
            color = NpTheme.colors.textSecondary,
            modifier = Modifier.width(80.dp),
        )
        Text(
            value,
            style = MaterialTheme.typography.bodyMedium,
            color = NpTheme.colors.textPrimary,
        )
    }
}
