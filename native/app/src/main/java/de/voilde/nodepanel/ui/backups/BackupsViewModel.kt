package de.voilde.nodepanel.ui.backups

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.BackupNowRequest
import de.voilde.nodepanel.data.api.BackupView
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.RestoreJobView
import de.voilde.nodepanel.data.api.RestoreRequest
import de.voilde.nodepanel.data.api.ScheduleRequest
import de.voilde.nodepanel.data.api.ScheduleView
import de.voilde.nodepanel.data.api.TargetView
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive

/** Editable schedule form state, shared by create and edit. */
data class ScheduleForm(
    val editing: ScheduleView? = null, // null = create
    val type: String = "backup",
    val nodeId: String = "",
    val paths: String = "", // comma separated, backup type
    val label: String = "", // container_update type
    val name: String = "",
    val targetIds: Set<String> = emptySet(),
    val days: Set<Int> = emptySet(), // cron DOW: 0=Sunday
    val hour: String = "3",
    val minute: String = "0",
    val enabled: Boolean = true,
)

data class BackupsUiState(
    val loading: Boolean = true,
    val backups: List<BackupView> = emptyList(),
    val schedules: List<ScheduleView> = emptyList(),
    val restoreJobs: List<RestoreJobView> = emptyList(),
    val targets: List<TargetView> = emptyList(),
    val nodes: List<NodeView> = emptyList(),
    val error: String? = null,
    val message: String? = null,
    val actionInProgress: Boolean = false,
    val showBackupNow: Boolean = false,
    val restoreTarget: BackupView? = null,
    val deleteBackupTarget: BackupView? = null,
    val scheduleForm: ScheduleForm? = null,
    val deleteScheduleTarget: ScheduleView? = null,
)

class BackupsViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(BackupsUiState())
    val uiState: StateFlow<BackupsUiState> = _uiState

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val api = container.apiClient.api()
                coroutineScope {
                    val backupsD = async { api.backups() }
                    val schedulesD = async { api.schedules() }
                    val jobsD = async { runCatching { api.restoreJobs() }.getOrDefault(emptyList()) }
                    val targetsD = async { runCatching { api.targets() }.getOrDefault(emptyList()) }
                    val nodesD = async { runCatching { api.nodes() }.getOrDefault(emptyList()) }
                    _uiState.update {
                        it.copy(
                            loading = false,
                            backups = backupsD.await(),
                            schedules = schedulesD.await(),
                            restoreJobs = jobsD.await(),
                            targets = targetsD.await(),
                            nodes = nodesD.await(),
                        )
                    }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    fun nodeName(id: String): String =
        _uiState.value.nodes.firstOrNull { it.id == id }
            ?.let { it.name.ifBlank { it.hostname.ifBlank { it.id.take(8) } } }
            ?: id.take(8)

    // --- backup now ---

    fun showBackupNow(show: Boolean) = _uiState.update { it.copy(showBackupNow = show) }

    fun backupNow(nodeId: String, paths: String, name: String, targetIds: Set<String>) {
        val pathList = paths.split(",", " ", "\n").map { it.trim() }.filter { it.isNotBlank() }
        if (nodeId.isBlank() || pathList.isEmpty()) {
            _uiState.update { it.copy(message = "请选择节点并填写备份路径") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().backupNow(
                    BackupNowRequest(
                        nodeId = nodeId,
                        paths = pathList,
                        targetIds = targetIds.toList(),
                        name = name.trim(),
                    ),
                )
                if (res.isSuccessful) {
                    _uiState.update { it.copy(showBackupNow = false, message = "备份任务已启动") }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "启动备份失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    // --- delete backup ---

    fun showDeleteBackup(backup: BackupView?) = _uiState.update { it.copy(deleteBackupTarget = backup) }

    fun deleteBackup() {
        val backup = _uiState.value.deleteBackupTarget ?: return
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().deleteBackup(backup.id)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(deleteBackupTarget = null, message = "已删除备份") }
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

    // --- restore (archive-level: extract onto a target node's path) ---

    fun showRestore(backup: BackupView?) = _uiState.update { it.copy(restoreTarget = backup) }

    fun restore(nodeId: String, dest: String) {
        val backup = _uiState.value.restoreTarget ?: return
        if (nodeId.isBlank() || dest.isBlank()) {
            _uiState.update { it.copy(message = "请选择目标节点并填写恢复路径") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api()
                    .restoreBackup(backup.id, RestoreRequest(nodeId = nodeId, dest = dest.trim()))
                if (res.isSuccessful) {
                    _uiState.update { it.copy(restoreTarget = null, message = "恢复任务已启动") }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "恢复失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    // --- schedules ---

    fun openScheduleForm(existing: ScheduleView?) {
        _uiState.update { it.copy(scheduleForm = existing?.toForm(container.apiClient.json) ?: ScheduleForm()) }
    }

    fun closeScheduleForm() = _uiState.update { it.copy(scheduleForm = null) }

    fun saveSchedule(form: ScheduleForm) {
        val hour = form.hour.toIntOrNull()
        val minute = form.minute.toIntOrNull()
        if (hour == null || hour !in 0..23 || minute == null || minute !in 0..59) {
            _uiState.update { it.copy(message = "时间格式不正确") }
            return
        }
        val config = buildConfig(form)
        if (config == null) {
            _uiState.update { it.copy(message = if (form.type == "backup") "请选择节点并填写路径" else "请选择节点") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val body = ScheduleRequest(
                    type = form.type,
                    nodeId = form.nodeId,
                    config = config,
                    days = form.days.sorted(),
                    hour = hour,
                    minute = minute,
                    enabled = form.enabled,
                )
                val api = container.apiClient.api()
                val res = if (form.editing == null) {
                    api.createSchedule(body)
                } else {
                    api.updateSchedule(form.editing.id, body)
                }
                if (res.isSuccessful) {
                    _uiState.update {
                        it.copy(scheduleForm = null, message = if (form.editing == null) "已创建计划" else "已保存计划")
                    }
                    refresh()
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

    fun toggleSchedule(schedule: ScheduleView) {
        viewModelScope.launch {
            try {
                val body = ScheduleRequest(
                    type = schedule.type,
                    nodeId = schedule.nodeId,
                    config = runCatching {
                        container.apiClient.json.decodeFromString<JsonObject>(schedule.config)
                    }.getOrDefault(JsonObject(emptyMap())),
                    cron = schedule.cron,
                    enabled = !schedule.enabled,
                )
                val res = container.apiClient.api().updateSchedule(schedule.id, body)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(message = if (schedule.enabled) "已停用" else "已启用") }
                    refresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = "操作失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }

    fun showDeleteSchedule(schedule: ScheduleView?) = _uiState.update { it.copy(deleteScheduleTarget = schedule) }

    fun deleteSchedule() {
        val schedule = _uiState.value.deleteScheduleTarget ?: return
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = container.apiClient.api().deleteSchedule(schedule.id)
                if (res.isSuccessful) {
                    _uiState.update { it.copy(deleteScheduleTarget = null, message = "已删除计划") }
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

    private fun buildConfig(form: ScheduleForm): JsonObject? {
        return when (form.type) {
            "backup" -> {
                val paths = form.paths.split(",", " ", "\n").map { it.trim() }.filter { it.isNotBlank() }
                if (form.nodeId.isBlank() || paths.isEmpty()) return null
                JsonObject(
                    buildMap {
                        put("paths", JsonArray(paths.map { JsonPrimitive(it) }))
                        if (form.targetIds.isNotEmpty()) {
                            put("target_ids", JsonArray(form.targetIds.map { JsonPrimitive(it) }))
                        }
                        if (form.name.isNotBlank()) put("name", JsonPrimitive(form.name.trim()))
                    },
                )
            }
            "container_update" -> {
                if (form.nodeId.isBlank()) return null
                JsonObject(
                    buildMap {
                        put("node_ids", JsonArray(listOf(JsonPrimitive(form.nodeId))))
                        if (form.label.isNotBlank()) put("label", JsonPrimitive(form.label.trim()))
                    },
                )
            }
            else -> null
        }
    }

    private fun ScheduleView.toForm(json: kotlinx.serialization.json.Json): ScheduleForm {
        val cfg = runCatching { json.parseToJsonElement(config) as? JsonObject }.getOrNull()
        val paths = cfg?.get("paths")?.let {
            runCatching { it.jsonArray.joinToString(",") { e -> e.jsonPrimitive.content } }.getOrDefault("")
        } ?: ""
        val targetIds = cfg?.get("target_ids")?.let {
            runCatching { it.jsonArray.map { e -> e.jsonPrimitive.content }.toSet() }.getOrDefault(emptySet())
        } ?: emptySet()
        val label = cfg?.get("label")?.let { runCatching { it.jsonPrimitive.content }.getOrDefault("") } ?: ""
        val name = cfg?.get("name")?.let { runCatching { it.jsonPrimitive.content }.getOrDefault("") } ?: ""
        // cron "M H * * D..." — days empty means daily
        val parts = cron.split(" ").filter { it.isNotBlank() }
        val minute = parts.getOrNull(0) ?: "0"
        val hour = parts.getOrNull(1) ?: "3"
        val days = parts.getOrNull(4)?.takeIf { it != "*" }
            ?.split(",")?.mapNotNull { it.toIntOrNull() }?.toSet() ?: emptySet()
        return ScheduleForm(
            editing = this,
            type = type.ifBlank { "backup" },
            nodeId = nodeId,
            paths = paths,
            label = label,
            name = name,
            targetIds = targetIds,
            days = days,
            hour = hour,
            minute = minute,
            enabled = enabled,
        )
    }
}
