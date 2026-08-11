package de.voilde.nodepanel.ui.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.AccountRequest
import de.voilde.nodepanel.data.api.CloudflareConfigRequest
import de.voilde.nodepanel.data.api.ContainerMonitorRequest
import de.voilde.nodepanel.data.api.DomainRequest
import de.voilde.nodepanel.data.api.ExcludesRequest
import de.voilde.nodepanel.data.api.KomariConfigRequest
import de.voilde.nodepanel.data.api.KomariTestRequest
import de.voilde.nodepanel.data.api.RetentionRequest
import de.voilde.nodepanel.data.api.TargetRequest
import de.voilde.nodepanel.data.api.TargetView
import de.voilde.nodepanel.data.api.TelegramConfigRequest
import de.voilde.nodepanel.data.api.backendError
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put

/** Editable backup-target form; one instance per create/edit dialog. */
data class TargetForm(
    val editing: TargetView? = null, // null = create
    val type: String = "github",
    val name: String = "",
    // github: server auto-derives owner/branch from the token
    val ghToken: String = "",
    val ghRepo: String = "",
    // s3
    val s3Endpoint: String = "",
    val s3Access: String = "",
    val s3Secret: String = "",
    val s3Bucket: String = "",
    val s3Prefix: String = "",
    val s3Region: String = "",
    val s3PathStyle: Boolean = true,
    val s3Secure: Boolean = true,
    // vps (sftp)
    val vpsHost: String = "",
    val vpsPort: String = "22",
    val vpsUser: String = "root",
    val vpsPassword: String = "",
    val vpsKeyPem: String = "",
    val vpsBaseDir: String = "",
)

data class SettingsUiState(
    val loading: Boolean = true,
    val error: String? = null,
    val message: String? = null,
    val busy: Boolean = false,
    val testing: Boolean = false,
    // account
    val username: String = "",
    // telegram
    val tgToken: String = "",
    val tgChatId: String = "",
    // backup: retention + excludes + container monitor
    val keepCount: String = "",
    val keepDays: String = "",
    val excludes: String = "", // one per line
    val monitorEnabled: Boolean = false,
    val monitorInterval: String = "60",
    // cloudflare + panel public url
    val publicUrl: String = "",
    val cfToken: String = "",
    // komari
    val komariBase: String = "",
    val komariKey: String = "",
    val komariInstall: String = "",
    // targets
    val targets: List<TargetView> = emptyList(),
    val targetForm: TargetForm? = null,
    val deleteTargetTarget: TargetView? = null,
)

class SettingsViewModel(private val container: AppContainer) : ViewModel() {

    private val api get() = container.apiClient.api()
    private val json get() = container.apiClient.json

    private val _uiState = MutableStateFlow(SettingsUiState())
    val uiState: StateFlow<SettingsUiState> = _uiState

    init { load() }

    fun load() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val raw = api.settings()
                val targets = runCatching { api.targets() }.getOrDefault(emptyList())
                _uiState.update { it.copy(loading = false, targets = targets) }
                applySettings(raw)
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = e.message ?: "加载失败") }
            }
        }
    }

    // GetAll returns a flat map of setting keys; each value is the parsed JSON
    // stored under that key (object / array / plain string).
    private fun applySettings(s: JsonObject) {
        fun obj(key: String): JsonObject? = s[key] as? JsonObject
        fun str(key: String): String = s[key]?.jsonPrimitive?.content ?: ""

        val tg = obj("telegram")
        val retention = obj("backup_retention")
        val monitor = obj("container_monitor")
        val cf = obj("cloudflare")
        val komari = obj("komari")
        val excludes = s["backup_excludes"]?.jsonArray
            ?.mapNotNull { runCatching { it.jsonPrimitive.content }.getOrNull() }
            ?: emptyList()

        _uiState.update {
            it.copy(
                username = obj("account")?.get("username")?.jsonPrimitive?.content ?: "",
                tgToken = tg?.get("bot_token")?.jsonPrimitive?.content ?: "",
                tgChatId = tg?.get("chat_id")?.jsonPrimitive?.content ?: "",
                keepCount = retention?.get("keep_count")?.jsonPrimitive?.intOrNull?.toString() ?: "",
                keepDays = retention?.get("keep_days")?.jsonPrimitive?.intOrNull?.toString() ?: "",
                excludes = excludes.joinToString("\n"),
                monitorEnabled = monitor?.get("enabled")?.jsonPrimitive?.booleanOrNull ?: false,
                monitorInterval = monitor?.get("interval_seconds")?.jsonPrimitive?.intOrNull?.toString() ?: "60",
                publicUrl = str("public_url"),
                cfToken = cf?.get("api_token")?.jsonPrimitive?.content ?: "",
                komariBase = komari?.get("base_url")?.jsonPrimitive?.content ?: "",
                komariKey = komari?.get("api_key")?.jsonPrimitive?.content ?: "",
                komariInstall = komari?.get("install_url")?.jsonPrimitive?.content ?: "",
            )
        }
    }

    fun clearMessage() = _uiState.update { it.copy(message = null) }

    // Generic mutation runner: busy flag + snackbar message, reload on success.
    private fun mutate(successMsg: String, block: suspend () -> String?) {
        if (_uiState.value.busy) return
        viewModelScope.launch {
            _uiState.update { it.copy(busy = true) }
            val err = try {
                block()
            } catch (e: Exception) {
                e.message ?: "请求失败"
            }
            _uiState.update { it.copy(busy = false, message = err ?: successMsg) }
        }
    }

    // --- account ---

    fun saveAccount(username: String, newPassword: String) {
        mutate("账户已更新") {
            val r = api.putAccount(AccountRequest(username, newPassword))
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(username = username) }
            null
        }
    }

    fun logout(onLoggedOut: () -> Unit) {
        viewModelScope.launch {
            runCatching { api.logout() }
            container.sessionManager.clearSession()
            onLoggedOut()
        }
    }

    // --- telegram ---

    fun saveTelegram(token: String, chatId: String) {
        mutate("Telegram 配置已保存") {
            val r = api.putTelegram(TelegramConfigRequest(token.trim(), chatId.trim()))
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(tgToken = token.trim(), tgChatId = chatId.trim()) }
            null
        }
    }

    fun testTelegram(token: String, chatId: String) {
        test {
            val r = api.testTelegram(TelegramConfigRequest(token.trim(), chatId.trim()))
            if (!r.isSuccessful) return@test r.backendError(json) to ""
            null to "测试消息已发送"
        }
    }

    // --- backup: retention / excludes / container monitor ---

    fun saveRetention(keepCount: String, keepDays: String) {
        mutate("保留策略已保存") {
            val r = api.putRetention(
                RetentionRequest(keepCount.toIntOrNull() ?: 0, keepDays.toIntOrNull() ?: 0),
            )
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(keepCount = keepCount, keepDays = keepDays) }
            null
        }
    }

    fun saveExcludes(text: String) {
        mutate("排除路径已保存") {
            val list = text.lines().map { it.trim() }.filter { it.isNotBlank() }
            val r = api.putExcludes(ExcludesRequest(list))
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(excludes = text) }
            null
        }
    }

    fun saveMonitor(enabled: Boolean, interval: String) {
        mutate("容器监控配置已保存") {
            val r = api.putContainerMonitor(
                ContainerMonitorRequest(enabled, interval.toIntOrNull() ?: 60),
            )
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(monitorEnabled = enabled, monitorInterval = interval) }
            null
        }
    }

    // --- cloudflare + domain ---

    fun saveDomain(url: String) {
        mutate("公网地址已保存") {
            val r = api.putDomain(DomainRequest(url.trim()))
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(publicUrl = url.trim()) }
            null
        }
    }

    fun saveCloudflare(token: String) {
        mutate("Cloudflare 令牌已保存") {
            val r = api.putCloudflare(CloudflareConfigRequest(token.trim()))
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(cfToken = token.trim()) }
            null
        }
    }

    fun testCloudflare(token: String) {
        test {
            val r = api.testCloudflare(CloudflareConfigRequest(token.trim()))
            if (!r.isSuccessful) return@test r.backendError(json) to ""
            val body = r.body()
            null to "令牌有效：账号 ${body?.accountId ?: "-"}，${body?.count ?: 0} 个隧道"
        }
    }

    // --- komari ---

    fun saveKomari(base: String, key: String, install: String) {
        mutate("Komari 配置已保存") {
            val r = api.putKomari(
                KomariConfigRequest(base.trim(), key.trim(), install.trim()),
            )
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update {
                it.copy(komariBase = base.trim(), komariKey = key.trim(), komariInstall = install.trim())
            }
            null
        }
    }

    fun testKomari(base: String, key: String) {
        test {
            val r = api.testKomari(KomariTestRequest(base.trim(), key.trim()))
            if (!r.isSuccessful) return@test r.backendError(json) to ""
            null to "连接成功，${r.body()?.count ?: 0} 个客户端"
        }
    }

    /** Test runner: success message / backend error via snackbar. */
    private fun test(block: suspend () -> Pair<String?, String>) {
        if (_uiState.value.testing) return
        viewModelScope.launch {
            _uiState.update { it.copy(testing = true) }
            val (err, okMsg) = try {
                block()
            } catch (e: Exception) {
                (e.message ?: "请求失败") to ""
            }
            _uiState.update { it.copy(testing = false, message = err ?: okMsg) }
        }
    }

    // --- targets ---

    fun openTargetForm(existing: TargetView?) {
        if (existing == null) {
            _uiState.update { it.copy(targetForm = TargetForm()) }
            return
        }
        val cfg = runCatching {
            json.parseToJsonElement(existing.config).jsonObject
        }.getOrNull()
        fun cfgStr(key: String): String = cfg?.get(key)?.jsonPrimitive?.content ?: ""
        _uiState.update {
            it.copy(
                targetForm = TargetForm(
                    editing = existing,
                    type = existing.type,
                    name = existing.name,
                    ghToken = cfgStr("token"),
                    ghRepo = cfgStr("repo"),
                    s3Endpoint = cfgStr("endpoint"),
                    s3Access = cfgStr("access_key"),
                    s3Secret = cfgStr("secret_key"),
                    s3Bucket = cfgStr("bucket"),
                    s3Prefix = cfgStr("prefix"),
                    s3Region = cfgStr("region"),
                    s3PathStyle = cfg?.get("path_style")?.jsonPrimitive?.booleanOrNull ?: true,
                    s3Secure = cfg?.get("secure")?.jsonPrimitive?.booleanOrNull ?: true,
                    vpsHost = cfgStr("host"),
                    vpsPort = cfg?.get("port")?.jsonPrimitive?.intOrNull?.toString() ?: "22",
                    vpsUser = cfgStr("user").ifBlank { "root" },
                    vpsPassword = cfgStr("password"),
                    vpsKeyPem = cfgStr("key_pem"),
                    vpsBaseDir = cfgStr("base_dir"),
                ),
            )
        }
    }

    fun closeTargetForm() = _uiState.update { it.copy(targetForm = null) }

    fun saveTarget(form: TargetForm) {
        val config = buildJsonObject {
            when (form.type) {
                "github" -> {
                    put("token", form.ghToken.trim())
                    put("repo", form.ghRepo.trim())
                }
                "s3" -> {
                    put("endpoint", form.s3Endpoint.trim())
                    put("access_key", form.s3Access.trim())
                    put("secret_key", form.s3Secret.trim())
                    put("bucket", form.s3Bucket.trim())
                    put("prefix", form.s3Prefix.trim())
                    put("region", form.s3Region.trim())
                    put("path_style", form.s3PathStyle)
                    put("secure", form.s3Secure)
                }
                "vps" -> {
                    put("host", form.vpsHost.trim())
                    put("port", form.vpsPort.toIntOrNull() ?: 22)
                    put("user", form.vpsUser.trim())
                    put("password", form.vpsPassword)
                    put("key_pem", form.vpsKeyPem)
                    put("base_dir", form.vpsBaseDir.trim())
                }
            }
        }
        mutate(if (form.editing == null) "目标已创建" else "目标已保存") {
            val editing = form.editing
            val err = if (editing == null) {
                val r = api.createTarget(
                    TargetRequest(type = form.type, name = form.name.trim(), config = config, enabled = true),
                )
                if (!r.isSuccessful) r.backendError(json) else null
            } else {
                val r = api.updateTarget(
                    editing.id,
                    TargetRequest(type = form.type, name = form.name.trim(), config = config, enabled = editing.enabled),
                )
                if (!r.isSuccessful) r.backendError(json) else null
            }
            if (err != null) return@mutate err
            closeTargetForm()
            refreshTargets()
            null
        }
    }

    fun toggleTarget(t: TargetView) {
        mutate(if (t.enabled) "已停用" else "已启用") {
            val cfg = runCatching { json.parseToJsonElement(t.config).jsonObject }
                .getOrElse { buildJsonObject {} }
            val r = api.updateTarget(
                t.id,
                TargetRequest(type = t.type, name = t.name, config = cfg, enabled = !t.enabled),
            )
            if (!r.isSuccessful) return@mutate r.backendError(json)
            refreshTargets()
            null
        }
    }

    fun showDeleteTarget(t: TargetView?) = _uiState.update { it.copy(deleteTargetTarget = t) }

    fun deleteTarget() {
        val t = _uiState.value.deleteTargetTarget ?: return
        mutate("目标已删除") {
            val r = api.deleteTarget(t.id)
            if (!r.isSuccessful) return@mutate r.backendError(json)
            _uiState.update { it.copy(deleteTargetTarget = null) }
            refreshTargets()
            null
        }
    }

    fun testTarget(id: String) {
        test {
            val r = api.testTarget(id)
            if (!r.isSuccessful) return@test r.backendError(json) to ""
            null to "连接成功"
        }
    }

    private suspend fun refreshTargets() {
        val targets = runCatching { api.targets() }.getOrDefault(emptyList())
        _uiState.update { it.copy(targets = targets) }
    }
}
