package de.voilde.nodepanel.ui.nodes

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.FirewallInfo
import de.voilde.nodepanel.data.api.FirewallNodeRequest
import de.voilde.nodepanel.data.api.FirewallPortsRequest
import de.voilde.nodepanel.data.api.FirewallToggleRequest
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.RenameNodeRequest
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class NodesUiState(
    val loading: Boolean = true,
    val nodes: List<NodeView> = emptyList(),
    val error: String? = null,
    val message: String? = null, // snackbar, cleared after display
    val actionInProgress: Boolean = false,
    val renameTarget: NodeView? = null,
    val deleteTarget: NodeView? = null,
    val firewallNode: NodeView? = null,
    val firewall: FirewallInfo? = null,
    val firewallLoading: Boolean = false,
)

class NodesViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(NodesUiState())
    val uiState: StateFlow<NodesUiState> = _uiState

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val nodes = container.apiClient.api().nodes()
                _uiState.update { it.copy(loading = false, nodes = nodes) }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    // --- rename ---

    fun showRename(node: NodeView) = _uiState.update { it.copy(renameTarget = node) }
    fun dismissRename() = _uiState.update { it.copy(renameTarget = null) }

    fun rename(name: String) {
        val node = _uiState.value.renameTarget ?: return
        if (name.isBlank()) {
            _uiState.update { it.copy(message = "名称不能为空") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api()
                    .renameNode(node.id, RenameNodeRequest(name = name.trim(), sshPort = node.sshPort))
                if (res.isSuccessful) {
                    _uiState.update { it.copy(renameTarget = null, message = "已改名") }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "改名失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    // --- delete ---

    fun showDelete(node: NodeView) = _uiState.update { it.copy(deleteTarget = node) }
    fun dismissDelete() = _uiState.update { it.copy(deleteTarget = null) }

    fun delete() {
        val node = _uiState.value.deleteTarget ?: return
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().deleteNode(node.id)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(deleteTarget = null, message = "已删除节点 ${node.name}") }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "删除失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    // --- firewall ---

    fun openFirewall(node: NodeView) {
        _uiState.update { it.copy(firewallNode = node, firewall = null, firewallLoading = true) }
        loadFirewall(node.id)
    }

    fun dismissFirewall() = _uiState.update { it.copy(firewallNode = null, firewall = null) }

    private fun loadFirewall(nodeId: String) {
        viewModelScope.launch {
            try {
                val info = container.apiClient.api().firewallStatus(FirewallNodeRequest(nodeId))
                _uiState.update { it.copy(firewall = info, firewallLoading = false) }
            } catch (e: Exception) {
                _uiState.update {
                    it.copy(firewallLoading = false, message = "读取防火墙状态失败：${e.message ?: e.javaClass.simpleName}")
                }
            }
        }
    }

    fun toggleFirewall() {
        val node = _uiState.value.firewallNode ?: return
        val current = _uiState.value.firewall ?: return
        val action = if (current.active) "disable" else "enable"
        viewModelScope.launch {
            _uiState.update { it.copy(firewallLoading = true) }
            try {
                val info = container.apiClient.api()
                    .firewallToggle(FirewallToggleRequest(node.id, action))
                _uiState.update {
                    it.copy(
                        firewall = info,
                        firewallLoading = false,
                        message = info.error?.takeIf { e -> e.isNotBlank() }
                            ?: if (info.active) "防火墙已开启" else "防火墙已关闭",
                    )
                }
            } catch (e: Exception) {
                _uiState.update {
                    it.copy(firewallLoading = false, message = "操作失败：${e.message ?: e.javaClass.simpleName}")
                }
            }
        }
    }

    fun setPort(port: String, proto: String, open: Boolean) {
        val node = _uiState.value.firewallNode ?: return
        val spec = "$port/$proto"
        val action = if (open) "allow" else "deny"
        viewModelScope.launch {
            _uiState.update { it.copy(firewallLoading = true) }
            try {
                val info = container.apiClient.api()
                    .firewallPorts(FirewallPortsRequest(node.id, listOf(spec), action))
                _uiState.update {
                    it.copy(
                        firewall = info,
                        firewallLoading = false,
                        message = info.error?.takeIf { e -> e.isNotBlank() }
                            ?: "已${if (open) "开放" else "关闭"}端口 $spec",
                    )
                }
            } catch (e: Exception) {
                _uiState.update {
                    it.copy(firewallLoading = false, message = "端口操作失败：${e.message ?: e.javaClass.simpleName}")
                }
            }
        }
    }
}
