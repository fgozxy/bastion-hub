package de.voilde.nodepanel.ui.nodes

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.CreateNodeRequest
import de.voilde.nodepanel.data.api.backendError
import de.voilde.nodepanel.ui.theme.NpTheme
import kotlinx.coroutines.launch

/**
 * Add-node dialog, mirrors web Nodes.tsx: name input → POST /api/nodes →
 * show install_cmd (selectable) + copy button. Self-contained so both the
 * dashboard top bar and the nodes page can drop it in.
 */
@Composable
fun AddNodeDialog(
    container: AppContainer,
    onDismiss: () -> Unit,
    onCreated: () -> Unit = {},
    onCopied: () -> Unit = {},
) {
    var name by remember { mutableStateOf("") }
    var installCmd by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    val clipboard = LocalClipboardManager.current

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("添加节点") },
        text = {
            if (installCmd.isBlank()) {
                Column {
                    OutlinedTextField(
                        value = name,
                        onValueChange = { name = it },
                        label = { Text("节点名称（可稍后修改）") },
                        placeholder = { Text("例如：东京-1") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    error?.let {
                        Spacer(Modifier.height(6.dp))
                        Text(it, color = NpTheme.colors.warning, style = MaterialTheme.typography.bodySmall)
                    }
                }
            } else {
                Column {
                    Text(
                        "在目标主机以 root 执行以下命令，Agent 会自动安装并加入面板：",
                        style = MaterialTheme.typography.bodySmall,
                        color = NpTheme.colors.textSecondary,
                    )
                    Spacer(Modifier.height(8.dp))
                    SelectionContainer {
                        Text(
                            installCmd,
                            style = MaterialTheme.typography.bodySmall.copy(
                                fontFamily = FontFamily.Monospace,
                                fontSize = 12.sp,
                            ),
                            color = NpTheme.colors.textPrimary,
                            modifier = Modifier
                                .fillMaxWidth()
                                .background(NpTheme.colors.bgTertiary, RoundedCornerShape(8.dp))
                                .padding(10.dp),
                        )
                    }
                    Spacer(Modifier.height(6.dp))
                    Text(
                        "支持主流 Linux 发行版；Agent 通过 WSS 反向连接，节点无需开放入站端口。",
                        style = MaterialTheme.typography.labelSmall,
                        color = NpTheme.colors.textTertiary,
                    )
                }
            }
        },
        confirmButton = {
            if (installCmd.isBlank()) {
                TextButton(
                    onClick = {
                        scope.launch {
                            busy = true
                            error = null
                            try {
                                val res = container.apiClient.api()
                                    .createNode(CreateNodeRequest(name.trim()))
                                if (res.isSuccessful) {
                                    installCmd = res.body()?.installCmd.orEmpty()
                                    onCreated()
                                } else {
                                    error = res.backendError(container.apiClient.json)
                                }
                            } catch (e: Exception) {
                                error = "创建失败：${e.message ?: e.javaClass.simpleName}"
                            } finally {
                                busy = false
                            }
                        }
                    },
                    enabled = !busy,
                ) { Text(if (busy) "创建中…" else "生成安装命令") }
            } else {
                TextButton(
                    onClick = {
                        clipboard.setText(AnnotatedString(installCmd))
                        onCopied()
                    },
                ) { Text("复制") }
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text(if (installCmd.isBlank()) "取消" else "关闭") }
        },
    )
}
