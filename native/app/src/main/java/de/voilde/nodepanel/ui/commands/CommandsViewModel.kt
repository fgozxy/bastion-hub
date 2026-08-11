package de.voilde.nodepanel.ui.commands

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.CommandLineView
import de.voilde.nodepanel.data.api.CommandView
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.RunCommandRequest
import de.voilde.nodepanel.data.api.SavedCommandRequest
import de.voilde.nodepanel.data.api.SavedCommandView
import de.voilde.nodepanel.data.api.WsMessage
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.double
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.WebSocket
import okhttp3.WebSocketListener

/** One streamed output line for the live command view. */
data class OutputLine(
    val stream: String,
    val data: String,
)

/** Live (or replayed) view of one command across its target nodes. */
data class ActiveCommand(
    val id: String,
    val cmd: String,
    val nodeIds: List<String>,
    val lines: Map<String, List<OutputLine>> = emptyMap(),
    val exits: Map<String, Int> = emptyMap(),
    // "" = still running (live only); completed | failed | history
    val status: String = "",
)

data class RunForm(
    val cmd: String = "",
    val nodeIds: Set<String> = emptySet(),
    val timeout: String = "300",
)

data class CommandsUiState(
    val loading: Boolean = true,
    val nodes: List<NodeView> = emptyList(),
    val saved: List<SavedCommandView> = emptyList(),
    val history: List<CommandView> = emptyList(),
    val error: String? = null,
    val message: String? = null,
    val actionInProgress: Boolean = false,
    val form: RunForm = RunForm(),
    val confirmRun: Boolean = false,
    val active: ActiveCommand? = null,
)

private const val MAX_LINES_PER_NODE = 2000

class CommandsViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(CommandsUiState())
    val uiState: StateFlow<CommandsUiState> = _uiState

    private var socket: WebSocket? = null
    private var closed = false

    init {
        refresh()
        connectWs()
    }

    override fun onCleared() {
        closed = true
        socket?.close(1000, "view cleared")
        socket = null
    }

    // --- browser websocket: command.output / command.done / command.finished ---

    private fun connectWs() {
        if (closed) return
        socket = container.apiClient.newBrowserWebSocket(object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) {
                handleWsMessage(text)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: okhttp3.Response?) {
                reconnectLater()
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                reconnectLater()
            }
        })
    }

    private fun reconnectLater() {
        if (closed) return
        viewModelScope.launch {
            delay(3000)
            if (!closed) connectWs()
        }
    }

    private fun handleWsMessage(text: String) {
        val msg = runCatching {
            container.apiClient.json.decodeFromString(WsMessage.serializer(), text)
        }.getOrNull() ?: return
        val data = msg.data ?: return
        when (msg.type) {
            "command.output" -> {
                val commandId = data["command_id"]?.jsonPrimitive?.content ?: return
                val nodeId = data["node_id"]?.jsonPrimitive?.content ?: return
                val stream = data["stream"]?.jsonPrimitive?.content ?: "stdout"
                val chunk = data["data"]?.jsonPrimitive?.content ?: return
                _uiState.update { s ->
                    val active = s.active?.takeIf { it.id == commandId } ?: return@update s
                    val nodeLines = active.lines[nodeId].orEmpty().let {
                        if (it.size >= MAX_LINES_PER_NODE) it else it + OutputLine(stream, chunk)
                    }
                    s.copy(active = active.copy(lines = active.lines + (nodeId to nodeLines)))
                }
            }
            "command.done" -> {
                val commandId = data["command_id"]?.jsonPrimitive?.content ?: return
                val nodeId = data["node_id"]?.jsonPrimitive?.content ?: return
                val exit = data["exit"]?.jsonPrimitive?.int ?: -1
                _uiState.update { s ->
                    val active = s.active?.takeIf { it.id == commandId } ?: return@update s
                    s.copy(active = active.copy(exits = active.exits + (nodeId to exit)))
                }
            }
            "command.finished" -> {
                val commandId = data["command_id"]?.jsonPrimitive?.content ?: return
                val status = data["status"]?.jsonPrimitive?.content ?: "completed"
                _uiState.update { s ->
                    val active = s.active?.takeIf { it.id == commandId } ?: return@update s
                    s.copy(active = active.copy(status = status))
                }
                refreshHistory()
            }
        }
    }

    // --- data ---

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val api = container.apiClient.api()
                val nodes = runCatching { api.nodes() }.getOrDefault(emptyList())
                val saved = runCatching { api.savedCommands() }.getOrDefault(emptyList())
                val history = api.commands()
                _uiState.update { it.copy(loading = false, nodes = nodes, saved = saved, history = history) }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    private fun refreshHistory() {
        viewModelScope.launch {
            val history = runCatching { container.apiClient.api().commands() }.getOrNull() ?: return@launch
            _uiState.update { it.copy(history = history) }
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    fun nodeName(id: String): String =
        _uiState.value.nodes.firstOrNull { it.id == id }
            ?.let { it.name.ifBlank { it.hostname.ifBlank { it.id.take(8) } } }
            ?: id.take(8)

    /** store.Command.node_ids is a JSON string — parse for display. */
    fun parseNodeIds(raw: String): List<String> =
        runCatching {
            container.apiClient.json.decodeFromString<List<String>>(raw)
        }.getOrDefault(emptyList())

    // --- run form ---

    fun setCmd(cmd: String) = _uiState.update { it.copy(form = it.form.copy(cmd = cmd)) }
    fun setTimeout(t: String) = _uiState.update { it.copy(form = it.form.copy(timeout = t)) }

    fun toggleNode(id: String) = _uiState.update {
        val cur = it.form.nodeIds
        it.copy(form = it.form.copy(nodeIds = if (id in cur) cur - id else cur + id))
    }

    fun applySaved(saved: SavedCommandView) = setCmd(saved.script)

    fun requestRun() {
        val form = _uiState.value.form
        if (form.cmd.isBlank() || form.nodeIds.isEmpty()) {
            _uiState.update { it.copy(message = "请填写命令并选择节点") }
            return
        }
        _uiState.update { it.copy(confirmRun = true) }
    }

    fun cancelRun() = _uiState.update { it.copy(confirmRun = false) }

    fun confirmRun() {
        val form = _uiState.value.form
        _uiState.update { it.copy(confirmRun = false) }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().runCommand(
                    RunCommandRequest(
                        nodeIds = form.nodeIds.toList(),
                        cmd = form.cmd,
                        timeout = form.timeout.toIntOrNull() ?: 300,
                    ),
                )
                if (res.isSuccessful) {
                    val id = res.body()?.id ?: ""
                    _uiState.update {
                        it.copy(
                            active = ActiveCommand(id = id, cmd = form.cmd, nodeIds = form.nodeIds.toList()),
                        )
                    }
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "执行失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    fun closeActive() = _uiState.update { it.copy(active = null) }

    // --- history ---

    fun openHistory(command: CommandView) {
        viewModelScope.launch {
            try {
                val detail = container.apiClient.api().commandDetail(command.id)
                val lines = detail.lines.groupBy(CommandLineView::nodeId)
                    .mapValues { (_, v) -> v.sortedBy { l -> l.seq }.map { OutputLine(it.stream, it.data) } }
                _uiState.update {
                    it.copy(
                        active = ActiveCommand(
                            id = command.id,
                            cmd = command.cmd,
                            nodeIds = parseNodeIds(command.nodeIds),
                            lines = lines,
                            status = "history:${command.status}",
                        ),
                    )
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "读取详情失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    // --- saved commands ---

    fun saveCurrentAs(name: String) {
        val cmd = _uiState.value.form.cmd
        if (name.isBlank() || cmd.isBlank()) {
            _uiState.update { it.copy(message = "请填写名称与命令") }
            return
        }
        viewModelScope.launch {
            try {
                val res = container.apiClient.api()
                    .createSavedCommand(SavedCommandRequest(name.trim(), cmd))
                if (res.isSuccessful) {
                    _uiState.update { it.copy(message = "已存为常用命令") }
                    val saved = runCatching { container.apiClient.api().savedCommands() }.getOrNull()
                    if (saved != null) _uiState.update { it.copy(saved = saved) }
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "保存失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun deleteSaved(saved: SavedCommandView) {
        viewModelScope.launch {
            try {
                val res = container.apiClient.api().deleteSavedCommand(saved.id)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(saved = it.saved.filter { s -> s.id != saved.id }, message = "已删除") }
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "删除失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }
}
