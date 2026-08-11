package de.voilde.nodepanel.ui.health

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.HealthAlertView
import de.voilde.nodepanel.data.api.HealthNodeStatus
import de.voilde.nodepanel.data.api.HealthNodesRequest
import de.voilde.nodepanel.data.api.HealthOpResult
import de.voilde.nodepanel.data.api.HealthTemplateResponse
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** Alert rule create/edit dialog state. */
data class AlertForm(
    val nodeId: String,
    val nodeName: String,
    val editing: HealthAlertView? = null,
)

/** Install/uninstall confirmation. */
data class HealthOp(
    val node: HealthNodeStatus,
    val install: Boolean,
)

data class HealthUiState(
    val loading: Boolean = true,
    val nodes: List<HealthNodeStatus> = emptyList(),
    // node_id -> its alert rules, loaded lazily per expanded card
    val alerts: Map<String, List<HealthAlertView>> = emptyMap(),
    val expandedNodeId: String? = null,
    val template: HealthTemplateResponse? = null,
    val error: String? = null,
    val message: String? = null,
    val actionInProgress: Boolean = false,
    val op: HealthOp? = null,
    val alertForm: AlertForm? = null,
    val deleteAlert: HealthAlertView? = null,
)

// Metric keys accepted by the backend (Sample.Value switch) for alert rules.
val ALERT_METRICS = listOf("load1", "load5", "load15", "cpu", "iowait", "mem", "swap", "disk")

class HealthViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(HealthUiState())
    val uiState: StateFlow<HealthUiState> = _uiState

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val (nodes, template) = coroutineScope {
                    val nodesD = async { container.apiClient.api().healthStatus() }
                    val templateD = async {
                        runCatching { container.apiClient.api().healthTemplate() }.getOrNull()
                    }
                    nodesD.await() to templateD.await()
                }
                _uiState.update { it.copy(loading = false, nodes = nodes, template = template) }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    // --- install / uninstall ---

    fun requestOp(node: HealthNodeStatus, install: Boolean) =
        _uiState.update { it.copy(op = HealthOp(node, install)) }

    fun dismissOp() = _uiState.update { it.copy(op = null) }

    fun confirmOp() {
        val op = _uiState.value.op ?: return
        _uiState.update { it.copy(op = null, actionInProgress = true) }
        viewModelScope.launch {
            try {
                val api = container.apiClient.api()
                val body = HealthNodesRequest(listOf(op.node.nodeId))
                // These run long install/uninstall scripts on the node (200s
                // ceiling server-side), so use the long-timeout client.
                val results = if (op.install) {
                    container.apiClient.apiLong().healthInstall(body)
                } else {
                    container.apiClient.apiLong().healthUninstall(body)
                }
                _uiState.update { it.copy(message = opResultMessage(op.install, results)) }
                refresh()
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "操作失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    private fun opResultMessage(install: Boolean, results: List<HealthOpResult>): String {
        val what = if (install) "安装" else "卸载"
        val r = results.firstOrNull() ?: return "${what}请求已发送"
        return if (r.ok) "Netdata ${what}成功" else "Netdata ${what}失败：${r.err.ifBlank { "未知错误" }}"
    }

    // --- alerts ---

    fun toggleExpand(nodeId: String) {
        val expanded = _uiState.value.expandedNodeId == nodeId
        _uiState.update { it.copy(expandedNodeId = if (expanded) null else nodeId) }
        if (!expanded) loadAlerts(nodeId)
    }

    private fun loadAlerts(nodeId: String) {
        viewModelScope.launch {
            runCatching { container.apiClient.api().healthAlerts(nodeId) }
                .onSuccess { list ->
                    _uiState.update { it.copy(alerts = it.alerts + (nodeId to list)) }
                }
                .onFailure { e ->
                    _uiState.update { it.copy(message = "读取告警规则失败：${e.message ?: e.javaClass.simpleName}") }
                }
        }
    }

    fun openAlertForm(node: HealthNodeStatus, editing: HealthAlertView?) =
        _uiState.update { it.copy(alertForm = AlertForm(node.nodeId, node.name, editing)) }

    fun closeAlertForm() = _uiState.update { it.copy(alertForm = null) }

    fun saveAlert(metric: String, threshold: String, windowSec: String, enabled: Boolean) {
        val form = _uiState.value.alertForm ?: return
        val th = threshold.toDoubleOrNull()
        val win = windowSec.toIntOrNull()
        if (metric.isBlank() || th == null || win == null || win < 0) {
            _uiState.update { it.copy(message = "请检查阈值与时间窗格式") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val body = HealthAlertView(
                    id = form.editing?.id ?: "",
                    nodeId = form.nodeId,
                    metric = metric,
                    threshold = th,
                    windowSec = win,
                    enabled = enabled,
                )
                val res = container.apiClient.api().putHealthAlert(body)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(alertForm = null, message = "已保存告警规则") }
                    loadAlerts(form.nodeId)
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "保存失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    fun showDeleteAlert(alert: HealthAlertView?) = _uiState.update { it.copy(deleteAlert = alert) }

    fun deleteAlert() {
        val alert = _uiState.value.deleteAlert ?: return
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().deleteHealthAlert(alert.id)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(deleteAlert = null, message = "已删除告警规则") }
                    loadAlerts(alert.nodeId)
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
}
