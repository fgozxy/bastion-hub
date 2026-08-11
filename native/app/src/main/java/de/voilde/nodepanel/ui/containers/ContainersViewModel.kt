package de.voilde.nodepanel.ui.containers

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.ContainerActionRequest
import de.voilde.nodepanel.data.api.ContainerResult
import de.voilde.nodepanel.data.api.ContainerView
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** A container op awaiting user confirmation (destructive/side-effecting ops). */
data class PendingContainerAction(
    val container: ContainerView,
    val action: String, // stop | restart | update
    val title: String,
    val text: String,
)

data class ContainersUiState(
    val loading: Boolean = true,
    val containers: List<ContainerView> = emptyList(),
    // node id -> display name, for labeling each container's host node
    val nodeNames: Map<String, String> = emptyMap(),
    val query: String = "",
    val error: String? = null,
    val message: String? = null, // snackbar, cleared after display
    val actionInProgress: Boolean = false,
    val pending: PendingContainerAction? = null,
)

class ContainersViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(ContainersUiState())
    val uiState: StateFlow<ContainersUiState> = _uiState

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val (containers, names) = coroutineScope {
                    val containersDeferred = async { container.apiClient.api().containers() }
                    val namesDeferred = async {
                        runCatching { container.apiClient.api().nodes() }
                            .getOrDefault(emptyList())
                            .associate { it.id to it.name.ifBlank { it.hostname.ifBlank { it.id } } }
                    }
                    containersDeferred.await() to namesDeferred.await()
                }
                _uiState.update { it.copy(loading = false, containers = containers, nodeNames = names) }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    fun setQuery(q: String) = _uiState.update { it.copy(query = q) }

    /** start runs immediately; stop/restart/update require confirmation. */
    fun requestAction(c: ContainerView, action: String) {
        when (action) {
            "start" -> runAction(c, action)
            "stop" -> _uiState.update {
                it.copy(pending = PendingContainerAction(c, action, "停止容器", "确定停止容器「${c.displayName.ifBlank { c.name }}」吗？服务将中断。"))
            }
            "restart" -> _uiState.update {
                it.copy(pending = PendingContainerAction(c, action, "重启容器", "确定重启容器「${c.displayName.ifBlank { c.name }}」吗？服务将短暂中断。"))
            }
            "update" -> _uiState.update {
                it.copy(pending = PendingContainerAction(c, action, "更新容器", "确定更新容器「${c.displayName.ifBlank { c.name }}」吗？将拉取最新镜像并重建容器。"))
            }
        }
    }

    fun confirmPending() {
        val pending = _uiState.value.pending ?: return
        _uiState.update { it.copy(pending = null) }
        runAction(pending.container, pending.action)
    }

    fun dismissPending() = _uiState.update { it.copy(pending = null) }

    private fun runAction(c: ContainerView, action: String) {
        if (_uiState.value.actionInProgress) return
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                // Single-container updates go through containers/action with an
                // explicit id — /api/container/update ignores ids and would
                // update ALL running containers on the node.
                val res = container.apiClient.api().containerAction(
                    ContainerActionRequest(nodeId = c.nodeId, ids = listOf(c.containerId), action = action),
                )
                if (res.isSuccessful) {
                    _uiState.update { it.copy(message = resultMessage(action, res.body())) }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "操作失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    private fun resultMessage(action: String, result: ContainerResult?): String {
        val actionLabel = when (action) {
            "start" -> "启动"
            "stop" -> "停止"
            "restart" -> "重启"
            "update" -> "更新"
            else -> action
        }
        if (result == null) return "${actionLabel}完成"
        if (!result.ok) return result.err.ifBlank { "${actionLabel}失败" }
        val failed = result.failed.orEmpty()
        return if (failed.isNotEmpty()) {
            "${actionLabel}部分失败：${failed.entries.joinToString("；") { (k, v) -> "$k: $v" }}"
        } else {
            "${actionLabel}成功"
        }
    }
}
