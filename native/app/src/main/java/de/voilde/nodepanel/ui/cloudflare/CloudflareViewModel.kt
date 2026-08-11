package de.voilde.nodepanel.ui.cloudflare

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.CreateTunnelRequest
import de.voilde.nodepanel.data.api.DnsRecord
import de.voilde.nodepanel.data.api.DnsRecordRequest
import de.voilde.nodepanel.data.api.DnsZone
import de.voilde.nodepanel.data.api.DomainMoveRequest
import de.voilde.nodepanel.data.api.DomainRule
import de.voilde.nodepanel.data.api.DomainRuleEditRequest
import de.voilde.nodepanel.data.api.DomainRuleRequest
import de.voilde.nodepanel.data.api.DomainTunnelView
import de.voilde.nodepanel.data.api.NodeView
import de.voilde.nodepanel.data.api.RenameTunnelRequest
import de.voilde.nodepanel.data.api.TunnelView
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** Create/edit form for one ingress rule; editing holds the original rule. */
data class RuleForm(
    val tunnel: DomainTunnelView,
    val editing: DomainRule? = null,
)

/** Move dialog state: hostname jumping to another tunnel. */
data class MoveForm(
    val hostname: String,
    val service: String,
    val fromTunnel: DomainTunnelView,
)

/** Create/edit form for one DNS record. */
data class RecordForm(
    val editing: DnsRecord? = null,
)

data class CloudflareUiState(
    val tab: Int = 0, // 0 隧道 / 1 域名 / 2 DNS
    val tunnelsLoading: Boolean = true,
    val tunnels: List<TunnelView> = emptyList(),
    val domainsLoading: Boolean = true,
    val domainTunnels: List<DomainTunnelView> = emptyList(),
    val zonesLoading: Boolean = true,
    val zones: List<DnsZone> = emptyList(),
    val selectedZoneId: String = "",
    val recordsLoading: Boolean = false,
    val records: List<DnsRecord> = emptyList(),
    val recordFilter: String = "",
    val nodes: List<NodeView> = emptyList(),
    val error: String? = null,
    val message: String? = null,
    val actionInProgress: Boolean = false,
    // dialogs
    val showCreateTunnel: Boolean = false,
    val renameTunnel: TunnelView? = null,
    val deleteTunnel: TunnelView? = null,
    val ruleForm: RuleForm? = null,
    val deleteRule: Pair<DomainTunnelView, DomainRule>? = null,
    val moveForm: MoveForm? = null,
    val recordForm: RecordForm? = null,
    val deleteRecord: DnsRecord? = null,
)

class CloudflareViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(CloudflareUiState())
    val uiState: StateFlow<CloudflareUiState> = _uiState

    init {
        refreshAll()
    }

    fun setTab(tab: Int) = _uiState.update { it.copy(tab = tab) }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    fun refreshAll() {
        refreshTunnels()
        refreshDomains()
        refreshZones()
        viewModelScope.launch {
            val nodes = runCatching { container.apiClient.api().nodes() }.getOrDefault(emptyList())
            _uiState.update { it.copy(nodes = nodes) }
        }
    }

    fun refreshTunnels() {
        viewModelScope.launch {
            _uiState.update { it.copy(tunnelsLoading = true) }
            runCatching { container.apiClient.api().tunnels() }
                .onSuccess { r -> _uiState.update { it.copy(tunnelsLoading = false, tunnels = r.tunnels) } }
                .onFailure { e ->
                    _uiState.update { it.copy(tunnelsLoading = false, message = failMsg("读取隧道列表", e)) }
                }
        }
    }

    fun refreshDomains() {
        viewModelScope.launch {
            _uiState.update { it.copy(domainsLoading = true) }
            runCatching { container.apiClient.api().domains() }
                .onSuccess { r -> _uiState.update { it.copy(domainsLoading = false, domainTunnels = r.tunnels) } }
                .onFailure { e ->
                    _uiState.update { it.copy(domainsLoading = false, message = failMsg("读取域名列表", e)) }
                }
        }
    }

    fun refreshZones() {
        viewModelScope.launch {
            _uiState.update { it.copy(zonesLoading = true) }
            runCatching { container.apiClient.api().dnsZones() }
                .onSuccess { r ->
                    _uiState.update {
                        it.copy(
                            zonesLoading = false,
                            zones = r.zones,
                            selectedZoneId = it.selectedZoneId.ifBlank { r.zones.firstOrNull()?.id ?: "" },
                        )
                    }
                    val zoneId = _uiState.value.selectedZoneId
                    if (zoneId.isNotBlank()) refreshRecords()
                }
                .onFailure { e ->
                    _uiState.update { it.copy(zonesLoading = false, message = failMsg("读取 zone 列表", e)) }
                }
        }
    }

    fun selectZone(zoneId: String) {
        _uiState.update { it.copy(selectedZoneId = zoneId, records = emptyList(), recordFilter = "") }
        refreshRecords()
    }

    fun refreshRecords() {
        val zoneId = _uiState.value.selectedZoneId
        if (zoneId.isBlank()) return
        viewModelScope.launch {
            _uiState.update { it.copy(recordsLoading = true) }
            runCatching { container.apiClient.api().dnsRecords(zoneId) }
                .onSuccess { r -> _uiState.update { it.copy(recordsLoading = false, records = r.records) } }
                .onFailure { e ->
                    _uiState.update { it.copy(recordsLoading = false, message = failMsg("读取 DNS 记录", e)) }
                }
        }
    }

    fun setRecordFilter(f: String) = _uiState.update { it.copy(recordFilter = f) }

    fun filteredRecords(): List<DnsRecord> {
        val s = _uiState.value
        val f = s.recordFilter.trim().lowercase()
        if (f.isEmpty()) return s.records
        return s.records.filter {
            it.name.lowercase().contains(f) ||
                it.content.lowercase().contains(f) ||
                it.type.lowercase().contains(f)
        }
    }

    // --- tunnel ops ---

    fun showCreateTunnel(show: Boolean) = _uiState.update { it.copy(showCreateTunnel = show) }

    fun createTunnel(nodeId: String, name: String) {
        if (nodeId.isBlank() || name.isBlank()) {
            _uiState.update { it.copy(message = "请选择节点并填写名称") }
            return
        }
        mutate(
            // Tunnel create installs cloudflared synchronously (~4min ceiling),
            // beyond the default 30s read timeout — use the long-timeout client.
            call = { container.apiClient.apiLong().createTunnel(CreateTunnelRequest(nodeId, name.trim())) },
            successMessage = { r -> r.status.ifBlank { "隧道已创建" } },
            closeDialog = { it.copy(showCreateTunnel = false) },
        )
    }

    fun showRenameTunnel(t: TunnelView?) = _uiState.update { it.copy(renameTunnel = t) }

    fun renameTunnel(name: String) {
        val t = _uiState.value.renameTunnel ?: return
        if (name.isBlank()) return
        mutate(
            call = { container.apiClient.api().renameTunnel(t.id, RenameTunnelRequest(name.trim())) },
            successMessage = { "已重命名" },
            closeDialog = { it.copy(renameTunnel = null) },
        )
    }

    fun showDeleteTunnel(t: TunnelView?) = _uiState.update { it.copy(deleteTunnel = t) }

    fun deleteTunnel() {
        val t = _uiState.value.deleteTunnel ?: return
        mutate(
            call = { container.apiClient.api().deleteTunnel(t.id) },
            // Delete returns partial-failure notes (offline node / DNS cleanup).
            successMessage = { r -> if (r.note.isNotBlank()) "已删除；${r.note}" else "已删除隧道" },
            closeDialog = { it.copy(deleteTunnel = null) },
        )
    }

    fun tunnelCtl(t: TunnelView, start: Boolean) {
        mutate(
            call = {
                if (start) container.apiClient.api().startTunnel(t.id)
                else container.apiClient.api().stopTunnel(t.id)
            },
            successMessage = { if (start) "已启动" else "已停止" },
        )
    }

    // --- domain rule ops ---

    fun openRuleForm(tunnel: DomainTunnelView, rule: DomainRule?) =
        _uiState.update { it.copy(ruleForm = RuleForm(tunnel, rule)) }

    fun closeRuleForm() = _uiState.update { it.copy(ruleForm = null) }

    fun saveRule(hostname: String, path: String, service: String) {
        val form = _uiState.value.ruleForm ?: return
        if (hostname.isBlank() || service.isBlank()) {
            _uiState.update { it.copy(message = "域名与指向(service)不能为空") }
            return
        }
        val editing = form.editing
        mutate(
            call = {
                if (editing == null) {
                    container.apiClient.api().addDomainRule(
                        DomainRuleRequest(form.tunnel.id, hostname.trim(), path.trim(), service.trim()),
                    )
                } else {
                    container.apiClient.api().editDomainRule(
                        DomainRuleEditRequest(
                            tunnelId = form.tunnel.id,
                            origHostname = editing.hostname,
                            origPath = editing.path,
                            hostname = hostname.trim(),
                            path = path.trim(),
                            service = service.trim(),
                        ),
                    )
                }
            },
            successMessage = { if (editing == null) "已添加规则" else "已保存规则" },
            closeDialog = { it.copy(ruleForm = null) },
            alsoRefresh = ::refreshDomains,
        )
    }

    fun showDeleteRule(pair: Pair<DomainTunnelView, DomainRule>?) = _uiState.update { it.copy(deleteRule = pair) }

    fun deleteRule() {
        val (tunnel, rule) = _uiState.value.deleteRule ?: return
        mutate(
            call = { container.apiClient.api().deleteDomainRule(tunnel.id, rule.hostname, rule.path) },
            successMessage = { "已删除规则（DNS 记录未动）" },
            closeDialog = { it.copy(deleteRule = null) },
            alsoRefresh = ::refreshDomains,
        )
    }

    fun openMoveForm(tunnel: DomainTunnelView, rule: DomainRule) =
        _uiState.update { it.copy(moveForm = MoveForm(rule.hostname, rule.service, tunnel)) }

    fun closeMoveForm() = _uiState.update { it.copy(moveForm = null) }

    fun moveRule(toTunnelId: String) {
        val form = _uiState.value.moveForm ?: return
        mutate(
            call = {
                container.apiClient.api().moveDomainRule(
                    DomainMoveRequest(form.hostname, form.fromTunnel.id, toTunnelId, form.service),
                )
            },
            successMessage = { r -> if (r.note.isNotBlank()) "已移动；${r.note}" else "已移动域名" },
            closeDialog = { it.copy(moveForm = null) },
            alsoRefresh = ::refreshDomains,
        )
    }

    // --- DNS record ops ---

    fun openRecordForm(record: DnsRecord?) = _uiState.update { it.copy(recordForm = RecordForm(record)) }
    fun closeRecordForm() = _uiState.update { it.copy(recordForm = null) }

    fun saveRecord(req: DnsRecordRequest) {
        val form = _uiState.value.recordForm ?: return
        val zoneId = _uiState.value.selectedZoneId
        val editing = form.editing
        mutate(
            call = {
                if (editing == null) {
                    container.apiClient.api().createDnsRecord(req.copy(zoneId = zoneId))
                } else {
                    container.apiClient.api().updateDnsRecord(editing.id, zoneId, req.copy(zoneId = zoneId))
                }
            },
            successMessage = { if (editing == null) "已新建记录" else "已保存记录" },
            closeDialog = { it.copy(recordForm = null) },
            alsoRefresh = ::refreshRecords,
        )
    }

    fun showDeleteRecord(record: DnsRecord?) = _uiState.update { it.copy(deleteRecord = record) }

    fun deleteRecord() {
        val record = _uiState.value.deleteRecord ?: return
        val zoneId = _uiState.value.selectedZoneId
        mutate(
            call = { container.apiClient.api().deleteDnsRecord(record.id, zoneId) },
            successMessage = { "已删除记录" },
            closeDialog = { it.copy(deleteRecord = null) },
            alsoRefresh = ::refreshRecords,
        )
    }

    // --- shared mutation helper ---

    private fun mutate(
        call: suspend () -> retrofit2.Response<de.voilde.nodepanel.data.api.CfActionResponse>,
        successMessage: (de.voilde.nodepanel.data.api.CfActionResponse) -> String,
        closeDialog: (CloudflareUiState) -> CloudflareUiState = { it },
        alsoRefresh: () -> Unit = { refreshTunnels(); refreshDomains() },
    ) {
        viewModelScope.launch {
            _uiState.update { it.copy(actionInProgress = true) }
            try {
                val res = call()
                if (res.isSuccessful) {
                    val body = res.body()
                    _uiState.update {
                        closeDialog(it).copy(message = body?.let(successMessage) ?: "操作成功")
                    }
                    alsoRefresh()
                } else {
                    _uiState.update { it.copy(message = res.backendError(container.apiClient.json)) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(message = failMsg("操作", e)) }
            } finally {
                _uiState.update { it.copy(actionInProgress = false) }
            }
        }
    }

    private fun failMsg(what: String, e: Throwable) = "$what 失败：${e.message ?: e.javaClass.simpleName}"
}
