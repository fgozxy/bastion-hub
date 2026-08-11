package de.voilde.nodepanel.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Backup
import androidx.compose.material.icons.filled.Dashboard
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.Storage
import androidx.compose.material.icons.filled.Terminal
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.ui.backups.BackupsScreen
import de.voilde.nodepanel.ui.cloudflare.CloudflareScreen
import de.voilde.nodepanel.ui.commands.CommandsScreen
import de.voilde.nodepanel.ui.components.LogoDot
import de.voilde.nodepanel.ui.health.HealthScreen
import de.voilde.nodepanel.ui.dashboard.DashboardScreen
import de.voilde.nodepanel.ui.login.LoginScreen
import de.voilde.nodepanel.ui.containers.ContainersScreen
import de.voilde.nodepanel.ui.nodes.NodesScreen
import de.voilde.nodepanel.ui.settings.SettingsScreen
import de.voilde.nodepanel.ui.theme.NpTheme
import de.voilde.nodepanel.ui.update.UpdateUiState
import de.voilde.nodepanel.ui.update.UpdateViewModel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

object Routes {
    const val SPLASH = "splash"
    const val LOGIN = "login"
    const val DASHBOARD = "dashboard"
    const val NODES = "nodes"
    const val CONTAINERS = "containers"
    const val BACKUPS = "backups"
    const val COMMANDS = "commands"
    const val CLOUDFLARE = "cloudflare"
    const val HEALTH = "health"
    const val SETTINGS = "settings"
}

@Composable
fun AppNavHost(container: AppContainer) {
    val navController = rememberNavController()
    val backStackEntry by navController.currentBackStackEntryAsState()
    val currentRoute = backStackEntry?.destination?.route
    val showBottomBar = currentRoute == Routes.DASHBOARD ||
        currentRoute == Routes.NODES ||
        currentRoute == Routes.CONTAINERS ||
        currentRoute == Routes.BACKUPS ||
        currentRoute == Routes.COMMANDS

    val updateViewModel: UpdateViewModel = viewModel { UpdateViewModel(container) }
    val updateState by updateViewModel.uiState.collectAsState()
    LaunchedEffect(Unit) { updateViewModel.silentCheck() }

    Scaffold(
        bottomBar = {
            if (showBottomBar) {
                NavigationBar(
                    containerColor = NpTheme.colors.bgCard,
                    tonalElevation = 0.dp,
                ) {
                    NavigationBarItem(
                        selected = currentRoute == Routes.DASHBOARD,
                        onClick = {
                            navController.navigate(Routes.DASHBOARD) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(Icons.Filled.Dashboard, contentDescription = null) },
                        label = { Text("仪表盘") },
                    )
                    NavigationBarItem(
                        selected = currentRoute == Routes.NODES,
                        onClick = {
                            navController.navigate(Routes.NODES) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(Icons.Filled.Dns, contentDescription = null) },
                        label = { Text("节点") },
                    )
                    NavigationBarItem(
                        selected = currentRoute == Routes.CONTAINERS,
                        onClick = {
                            navController.navigate(Routes.CONTAINERS) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(Icons.Filled.Storage, contentDescription = null) },
                        label = { Text("容器") },
                    )
                    NavigationBarItem(
                        selected = currentRoute == Routes.BACKUPS,
                        onClick = {
                            navController.navigate(Routes.BACKUPS) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(Icons.Filled.Backup, contentDescription = null) },
                        label = { Text("备份") },
                    )
                    NavigationBarItem(
                        selected = currentRoute == Routes.COMMANDS,
                        onClick = {
                            navController.navigate(Routes.COMMANDS) {
                                popUpTo(navController.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(Icons.Filled.Terminal, contentDescription = null) },
                        label = { Text("命令") },
                    )
                }
            }
        },
    ) { padding ->
        NavHost(
            navController = navController,
            startDestination = Routes.SPLASH,
            modifier = Modifier.padding(padding),
        ) {
            composable(Routes.SPLASH) {
                SplashScreen(
                    onLoggedIn = {
                        navController.navigate(Routes.DASHBOARD) {
                            popUpTo(Routes.SPLASH) { inclusive = true }
                        }
                    },
                    onLoggedOut = {
                        navController.navigate(Routes.LOGIN) {
                            popUpTo(Routes.SPLASH) { inclusive = true }
                        }
                    },
                    checkAuth = {
                        if (!container.sessionManager.hasSession()) return@SplashScreen false
                        runCatching {
                            withContext(Dispatchers.IO) { container.apiClient.api().me() }
                        }.getOrNull()?.authenticated == true
                    },
                )
            }
            composable(Routes.LOGIN) {
                LoginScreen(
                    container = container,
                    onLoginSuccess = {
                        navController.navigate(Routes.DASHBOARD) {
                            popUpTo(Routes.LOGIN) { inclusive = true }
                        }
                    },
                )
            }
            composable(Routes.DASHBOARD) {
                DashboardScreen(
                    container = container,
                    onCheckUpdate = { updateViewModel.checkNow() },
                    onOpenCloudflare = { navController.navigate(Routes.CLOUDFLARE) },
                    onOpenHealth = { navController.navigate(Routes.HEALTH) },
                    onOpenSettings = { navController.navigate(Routes.SETTINGS) },
                    currentVersion = updateState.currentVersionName,
                )
            }
            composable(Routes.NODES) { NodesScreen(container) }
            composable(Routes.CONTAINERS) { ContainersScreen(container) }
            composable(Routes.BACKUPS) { BackupsScreen(container) }
            composable(Routes.COMMANDS) { CommandsScreen(container) }
            composable(Routes.CLOUDFLARE) {
                CloudflareScreen(
                    container = container,
                    onBack = { navController.popBackStack() },
                )
            }
            composable(Routes.HEALTH) {
                HealthScreen(
                    container = container,
                    onBack = { navController.popBackStack() },
                )
            }
            composable(Routes.SETTINGS) {
                SettingsScreen(
                    container = container,
                    onBack = { navController.popBackStack() },
                    onLogout = {
                        navController.navigate(Routes.LOGIN) {
                            popUpTo(0) { inclusive = true }
                        }
                    },
                    onCheckUpdate = { updateViewModel.checkNow() },
                    currentVersion = updateState.currentVersionName,
                )
            }
        }
    }

    if (updateState.available != null && !updateState.dismissed) {
        UpdateDialog(
            state = updateState,
            onDownload = updateViewModel::downloadAndInstall,
            onInstall = { updateState.downloaded?.let(updateViewModel::install) },
            onDismiss = updateViewModel::dismiss,
        )
    }
}

@Composable
private fun UpdateDialog(
    state: UpdateUiState,
    onDownload: () -> Unit,
    onInstall: () -> Unit,
    onDismiss: () -> Unit,
) {
    val info = state.available ?: return
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("发现新版本 ${info.versionName}") },
        text = {
            Column {
                Text("当前版本 ${state.currentVersionName}", style = MaterialTheme.typography.bodySmall)
                if (info.notes.isNotBlank()) {
                    Spacer(Modifier.height(8.dp))
                    Text(info.notes, style = MaterialTheme.typography.bodyMedium)
                }
                if (state.downloading) {
                    Spacer(Modifier.height(12.dp))
                    LinearProgressIndicator(
                        progress = { state.progress / 100f },
                        modifier = Modifier.fillMaxWidth(),
                    )
                    Spacer(Modifier.height(4.dp))
                    Text("下载中 ${state.progress}%", style = MaterialTheme.typography.labelMedium)
                }
            }
        },
        confirmButton = {
            when {
                state.downloaded != null -> TextButton(onClick = onInstall) { Text("安装") }
                state.downloading -> TextButton(onClick = {}, enabled = false) { Text("下载中…") }
                else -> TextButton(onClick = onDownload) { Text("立即更新") }
            }
        },
        dismissButton = {
            if (!state.downloading) {
                TextButton(onClick = onDismiss) { Text("稍后") }
            }
        },
    )
}

@Composable
private fun SplashScreen(
    onLoggedIn: () -> Unit,
    onLoggedOut: () -> Unit,
    checkAuth: suspend () -> Boolean,
) {
    LaunchedEffect(Unit) {
        if (checkAuth()) onLoggedIn() else onLoggedOut()
    }
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(NpTheme.colors.bgPage),
        contentAlignment = Alignment.Center,
    ) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            LogoDot(size = 48.dp)
            Spacer(Modifier.height(20.dp))
            LinearProgressIndicator(
                modifier = Modifier.width(120.dp),
                color = NpTheme.colors.primary,
                trackColor = NpTheme.colors.bgTertiary,
            )
        }
    }
}
