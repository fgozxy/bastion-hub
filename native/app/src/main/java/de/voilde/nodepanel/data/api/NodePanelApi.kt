package de.voilde.nodepanel.data.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import retrofit2.Response
import retrofit2.http.Body
import retrofit2.http.DELETE
import retrofit2.http.GET
import retrofit2.http.PATCH
import retrofit2.http.POST
import retrofit2.http.PUT
import retrofit2.http.Path
import retrofit2.http.Query

// DTOs mirror the Go structs in master/internal (snake_case JSON fields).

@Serializable
data class LoginRequest(
    val username: String,
    val password: String,
)

@Serializable
data class LoginResponse(
    val ok: String? = null,
)

@Serializable
data class MeResponse(
    val authenticated: Boolean = false,
    val username: String? = null,
)

@Serializable
data class ErrorResponse(
    val error: String? = null,
)

// dashboard/service.go: Stats
@Serializable
data class DashboardStats(
    val nodes: NodeStats = NodeStats(),
    val commands: CountStats = CountStats(),
    val backups: BackupStats = BackupStats(),
    val credentials: Int = 0,
    val countries: List<CountryCount>? = null,
    val metrics: Map<String, MiniMetric>? = null,
    val recent: List<RecentCommand>? = null,
)

@Serializable
data class NodeStats(
    val total: Int = 0,
    val online: Int = 0,
)

@Serializable
data class CountStats(
    val total: Int = 0,
    val today: Int = 0,
)

@Serializable
data class BackupStats(
    val total: Int = 0,
    val success: Int = 0,
    val failed: Int = 0,
    val recent: List<MiniBackup>? = null,
)

@Serializable
data class MiniBackup(
    val id: String = "",
    @SerialName("node_id") val nodeId: String = "",
    val name: String = "",
    val size: Long = 0,
    val status: String = "",
    @SerialName("created_at") val createdAt: Long = 0,
)

@Serializable
data class CountryCount(
    val code: String = "",
    val count: Int = 0,
)

@Serializable
data class MiniMetric(
    val cpu: Double = 0.0,
    @SerialName("mem_used") val memUsed: Long = 0,
    @SerialName("mem_total") val memTotal: Long = 0,
    @SerialName("disk_used") val diskUsed: Long = 0,
    @SerialName("disk_total") val diskTotal: Long = 0,
    val load1: Double = 0.0,
)

@Serializable
data class RecentCommand(
    val id: String = "",
    val cmd: String = "",
    val status: String = "",
    val at: Long = 0,
)

// nodes/service.go: nodeView = store.Node + online flag
// install_cmd only comes back from Create (never in the bulk list).
@Serializable
data class NodeView(
    val id: String = "",
    val name: String = "",
    val status: String = "",
    val hostname: String = "",
    val os: String = "",
    val arch: String = "",
    val kernel: String = "",
    val ipv4: String = "",
    val ipv6: String = "",
    @SerialName("country_code") val countryCode: String = "",
    val country: String = "",
    @SerialName("agent_version") val agentVersion: String = "",
    @SerialName("last_seen") val lastSeen: Long = 0,
    @SerialName("created_at") val createdAt: Long = 0,
    @SerialName("ssh_port") val sshPort: String = "",
    @SerialName("install_cmd") val installCmd: String = "",
    val online: Boolean = false,
)

// nodes/service.go: Create POST /api/nodes {name} — server defaults to "New Node"
@Serializable
data class CreateNodeRequest(
    val name: String,
)

// nodes/service.go: Rename PATCH /api/nodes/{id} {name, ssh_port}
// ssh_port must echo the current value, otherwise RenameNode would clear it.
@Serializable
data class RenameNodeRequest(
    val name: String,
    @SerialName("ssh_port") val sshPort: String = "",
)

@Serializable
data class OkResponse(
    val ok: String? = null,
)

// nodes/service.go: firewallInfo / portInfo
@Serializable
data class FirewallInfo(
    @SerialName("node_id") val nodeId: String = "",
    val name: String = "",
    val type: String = "",
    val active: Boolean = false,
    val ports: List<PortInfo>? = null,
    val detail: String? = null,
    val error: String? = null,
)

@Serializable
data class PortInfo(
    val port: String = "",
    val proto: String = "",
    val open: Boolean = false,
)

@Serializable
data class FirewallNodeRequest(
    @SerialName("node_id") val nodeId: String,
)

@Serializable
data class FirewallToggleRequest(
    @SerialName("node_id") val nodeId: String,
    val action: String, // "enable" | "disable"
)

@Serializable
data class FirewallPortsRequest(
    @SerialName("node_id") val nodeId: String,
    val ports: List<String>, // ["80/tcp", "443"]
    val action: String, // "allow" | "deny"
)

// store.Container — container inventory across all nodes
@Serializable
data class ContainerView(
    @SerialName("node_id") val nodeId: String = "",
    @SerialName("container_id") val containerId: String = "",
    val name: String = "",
    @SerialName("display_name") val displayName: String = "",
    val image: String = "",
    @SerialName("image_id") val imageId: String = "",
    val state: String = "",
    val status: String = "",
    val created: Long = 0,
    val updated: Long = 0,
    @SerialName("update_type") val updateType: String = "",
    @SerialName("has_update") val hasUpdate: Int = 0, // -1 unknown, 0 current, 1 update available
    val note: String = "",
    @SerialName("scanned_at") val scannedAt: Long = 0,
    @SerialName("host_ports") val hostPorts: List<Int>? = null,
)

// container/service.go: Action POST /api/containers/action {node_id, ids, action, label, new_image}
@Serializable
data class ContainerActionRequest(
    @SerialName("node_id") val nodeId: String,
    val ids: List<String> = emptyList(),
    val action: String,
    val label: String = "",
    @SerialName("new_image") val newImage: String = "",
)

// proto.ContainerResult — agent reply for container ops
@Serializable
data class ContainerResult(
    val ok: Boolean = false,
    val err: String = "",
    val updated: List<String>? = null,
    val unchanged: List<String>? = null,
    val skipped: List<String>? = null,
    val failed: Map<String, String>? = null,
)

interface NodePanelApi {
    @POST("api/auth/login")
    suspend fun login(@Body body: LoginRequest): Response<LoginResponse>

    @GET("api/auth/me")
    suspend fun me(): MeResponse

    @GET("api/dashboard")
    suspend fun dashboard(): DashboardStats

    @GET("api/nodes")
    suspend fun nodes(): List<NodeView>

    @POST("api/nodes")
    suspend fun createNode(@Body body: CreateNodeRequest): Response<NodeView>

    @PATCH("api/nodes/{id}")
    suspend fun renameNode(@Path("id") id: String, @Body body: RenameNodeRequest): Response<OkResponse>

    @DELETE("api/nodes/{id}")
    suspend fun deleteNode(@Path("id") id: String): Response<OkResponse>

    @POST("api/nodes/firewall/status")
    suspend fun firewallStatus(@Body body: FirewallNodeRequest): FirewallInfo

    @POST("api/nodes/firewall/toggle")
    suspend fun firewallToggle(@Body body: FirewallToggleRequest): FirewallInfo

    @POST("api/nodes/firewall/ports")
    suspend fun firewallPorts(@Body body: FirewallPortsRequest): FirewallInfo

    @GET("api/containers")
    suspend fun containers(): List<ContainerView>

    @POST("api/containers/action")
    suspend fun containerAction(@Body body: ContainerActionRequest): Response<ContainerResult>

    @POST("api/container/update")
    suspend fun containerUpdate(@Body body: ContainerUpdateRequest): Response<ContainerResult>

    @GET("api/backups")
    suspend fun backups(): List<BackupView>

    @POST("api/backups/now")
    suspend fun backupNow(@Body body: BackupNowRequest): Response<IdResponse>

    @DELETE("api/backups/{id}")
    suspend fun deleteBackup(@Path("id") id: String): Response<OkResponse>

    @POST("api/backups/{id}/restore")
    suspend fun restoreBackup(@Path("id") id: String, @Body body: RestoreRequest): Response<IdResponse>

    @GET("api/restore/jobs")
    suspend fun restoreJobs(): List<RestoreJobView>

    @GET("api/schedules")
    suspend fun schedules(): List<ScheduleView>

    @POST("api/schedules")
    suspend fun createSchedule(@Body body: ScheduleRequest): Response<ScheduleView>

    @PUT("api/schedules/{id}")
    suspend fun updateSchedule(@Path("id") id: String, @Body body: ScheduleRequest): Response<OkResponse>

    @DELETE("api/schedules/{id}")
    suspend fun deleteSchedule(@Path("id") id: String): Response<OkResponse>

    @GET("api/targets")
    suspend fun targets(): List<TargetView>

    // --- settings + targets CRUD ---

    @POST("api/auth/logout")
    suspend fun logout(): Response<OkResponse>

    @GET("api/settings")
    suspend fun settings(): JsonObject

    @PUT("api/settings/account")
    suspend fun putAccount(@Body body: AccountRequest): Response<OkResponse>

    @PUT("api/settings/telegram")
    suspend fun putTelegram(@Body body: TelegramConfigRequest): Response<OkResponse>

    @POST("api/settings/telegram/test")
    suspend fun testTelegram(@Body body: TelegramConfigRequest): Response<OkResponse>

    @PUT("api/settings/retention")
    suspend fun putRetention(@Body body: RetentionRequest): Response<OkResponse>

    @PUT("api/settings/excludes")
    suspend fun putExcludes(@Body body: ExcludesRequest): Response<OkResponse>

    @PUT("api/settings/domain")
    suspend fun putDomain(@Body body: DomainRequest): Response<OkResponse>

    @PUT("api/settings/cloudflare")
    suspend fun putCloudflare(@Body body: CloudflareConfigRequest): Response<OkResponse>

    @POST("api/settings/cloudflare/test")
    suspend fun testCloudflare(@Body body: CloudflareConfigRequest): Response<CloudflareTestResponse>

    @PUT("api/settings/container-monitor")
    suspend fun putContainerMonitor(@Body body: ContainerMonitorRequest): Response<OkResponse>

    @PUT("api/settings/komari")
    suspend fun putKomari(@Body body: KomariConfigRequest): Response<OkResponse>

    @POST("api/settings/komari/test")
    suspend fun testKomari(@Body body: KomariTestRequest): Response<KomariTestResponse>

    @POST("api/targets")
    suspend fun createTarget(@Body body: TargetRequest): Response<TargetView>

    @PUT("api/targets/{id}")
    suspend fun updateTarget(@Path("id") id: String, @Body body: TargetRequest): Response<OkResponse>

    @DELETE("api/targets/{id}")
    suspend fun deleteTarget(@Path("id") id: String): Response<OkResponse>

    @POST("api/targets/{id}/test")
    suspend fun testTarget(@Path("id") id: String): Response<OkResponse>

    // --- Cloudflare: tunnels ---

    @GET("api/tunnels")
    suspend fun tunnels(): TunnelsResponse

    @POST("api/tunnels")
    suspend fun createTunnel(@Body body: CreateTunnelRequest): Response<CfActionResponse>

    @POST("api/tunnels/{id}/start")
    suspend fun startTunnel(@Path("id") id: String): Response<CfActionResponse>

    @POST("api/tunnels/{id}/stop")
    suspend fun stopTunnel(@Path("id") id: String): Response<CfActionResponse>

    @PATCH("api/tunnels/{id}")
    suspend fun renameTunnel(@Path("id") id: String, @Body body: RenameTunnelRequest): Response<CfActionResponse>

    @DELETE("api/tunnels/{id}")
    suspend fun deleteTunnel(@Path("id") id: String): Response<CfActionResponse>

    // --- Cloudflare: domains (tunnel ingress rules) ---

    @GET("api/domains")
    suspend fun domains(): DomainsResponse

    @POST("api/domains/rule")
    suspend fun addDomainRule(@Body body: DomainRuleRequest): Response<CfActionResponse>

    @PUT("api/domains/rule")
    suspend fun editDomainRule(@Body body: DomainRuleEditRequest): Response<CfActionResponse>

    @DELETE("api/domains/rule")
    suspend fun deleteDomainRule(
        @Query("tunnel_id") tunnelId: String,
        @Query("hostname") hostname: String,
        @Query("path") path: String,
    ): Response<CfActionResponse>

    @POST("api/domains/move")
    suspend fun moveDomainRule(@Body body: DomainMoveRequest): Response<CfActionResponse>

    // --- Cloudflare: DNS records ---

    @GET("api/dns/zones")
    suspend fun dnsZones(): DnsZonesResponse

    @GET("api/dns/records")
    suspend fun dnsRecords(@Query("zone_id") zoneId: String): DnsRecordsResponse

    @POST("api/dns/records")
    suspend fun createDnsRecord(@Body body: DnsRecordRequest): Response<CfActionResponse>

    @PUT("api/dns/records/{id}")
    suspend fun updateDnsRecord(
        @Path("id") id: String,
        @Query("zone_id") zoneId: String,
        @Body body: DnsRecordRequest,
    ): Response<CfActionResponse>

    @DELETE("api/dns/records/{id}")
    suspend fun deleteDnsRecord(
        @Path("id") id: String,
        @Query("zone_id") zoneId: String,
    ): Response<CfActionResponse>

    // --- commands ---

    @POST("api/commands")
    suspend fun runCommand(@Body body: RunCommandRequest): Response<IdResponse>

    @GET("api/commands")
    suspend fun commands(): List<CommandView>

    @GET("api/commands/{id}")
    suspend fun commandDetail(@Path("id") id: String): CommandDetailResponse

    @GET("api/commands/saved")
    suspend fun savedCommands(): List<SavedCommandView>

    @POST("api/commands/saved")
    suspend fun createSavedCommand(@Body body: SavedCommandRequest): Response<SavedCommandView>

    @DELETE("api/commands/saved/{id}")
    suspend fun deleteSavedCommand(@Path("id") id: String): Response<OkResponse>

    // --- health ---

    @GET("api/health")
    suspend fun healthStatus(): List<HealthNodeStatus>

    @POST("api/health/install")
    suspend fun healthInstall(@Body body: HealthNodesRequest): List<HealthOpResult>

    @POST("api/health/uninstall")
    suspend fun healthUninstall(@Body body: HealthNodesRequest): List<HealthOpResult>

    @GET("api/health/metrics")
    suspend fun healthMetrics(
        @Query("node_id") nodeId: String,
        @Query("window") window: Int = 180,
    ): List<HealthSample>

    @GET("api/health/alerts")
    suspend fun healthAlerts(@Query("node_id") nodeId: String): List<HealthAlertView>

    @PUT("api/health/alerts")
    suspend fun putHealthAlert(@Body body: HealthAlertView): Response<HealthAlertView>

    @DELETE("api/health/alerts/{id}")
    suspend fun deleteHealthAlert(@Path("id") id: String): Response<OkResponse>

    @GET("api/health/template")
    suspend fun healthTemplate(): HealthTemplateResponse
}

// --- commands/service.go ---

// Run POST /api/commands {node_ids, cmd, timeout}
@Serializable
data class RunCommandRequest(
    @SerialName("node_ids") val nodeIds: List<String>,
    val cmd: String,
    val timeout: Int = 300,
)

// store.Command — node_ids is a STRING holding a JSON array.
@Serializable
data class CommandView(
    val id: String = "",
    @SerialName("node_ids") val nodeIds: String = "", // JSON array string
    val cmd: String = "",
    val status: String = "", // running | completed | failed
    @SerialName("exit_code") val exitCode: Int = 0,
    val author: String = "",
    @SerialName("created_at") val createdAt: Long = 0,
    @SerialName("finished_at") val finishedAt: Long = 0,
)

// store.CommandLine
@Serializable
data class CommandLineView(
    @SerialName("node_id") val nodeId: String = "",
    val seq: Int = 0,
    val stream: String = "", // stdout | stderr
    val data: String = "",
)

@Serializable
data class CommandDetailResponse(
    val command: CommandView = CommandView(),
    val lines: List<CommandLineView> = emptyList(),
)

// store.SavedCommand
@Serializable
data class SavedCommandView(
    val id: String = "",
    val name: String = "",
    val script: String = "",
    val builtin: Boolean = false,
)

@Serializable
data class SavedCommandRequest(
    val name: String,
    val script: String,
)

// Browser WS (/api/ws) envelope: {"type": "...", "data": {...}}.
@Serializable
data class WsMessage(
    val type: String = "",
    val data: JsonObject? = null,
)

// --- health/service.go + cache.go + template.go ---

// health/service.go: nodeStatus
@Serializable
data class HealthNodeStatus(
    @SerialName("node_id") val nodeId: String = "",
    val name: String = "",
    val online: Boolean = false,
    @SerialName("agent_version") val agentVersion: String = "",
    @SerialName("supports_http_fetch") val supportsHttpFetch: Boolean = false,
    val enabled: Boolean = false,
    val installed: Boolean = false,
    val sample: HealthSample? = null,
)

// health/cache.go: Sample — precomputed scalars, no charts needed
@Serializable
data class HealthSample(
    val ts: Long = 0,
    val load1: Double = 0.0,
    val load5: Double = 0.0,
    val load15: Double = 0.0,
    val cpu: Double = 0.0, // busy %
    val iowait: Double = 0.0,
    @SerialName("mem_used_pct") val memUsedPct: Double = 0.0,
    @SerialName("swap_used_pct") val swapUsedPct: Double = 0.0,
    @SerialName("net_rx") val netRx: Double = 0.0, // KB/s
    @SerialName("net_tx") val netTx: Double = 0.0,
    @SerialName("disk_read") val diskRead: Double = 0.0,
    @SerialName("disk_write") val diskWrite: Double = 0.0,
    @SerialName("proc_running") val procRunning: Double = 0.0,
    @SerialName("proc_blocked") val procBlocked: Double = 0.0,
    val cores: Int = 0,
    @SerialName("disk_used_pct") val diskUsedPct: Double = 0.0,
)

// Install/Uninstall per-node result row
@Serializable
data class HealthOpResult(
    @SerialName("node_id") val nodeId: String = "",
    val name: String = "",
    val ok: Boolean = false,
    val err: String = "",
    val online: Boolean = false,
)

@Serializable
data class HealthNodesRequest(
    @SerialName("node_ids") val nodeIds: List<String>,
)

// store.HealthAlert — PutAlert takes the same shape (id empty = create)
@Serializable
data class HealthAlertView(
    val id: String = "",
    @SerialName("node_id") val nodeId: String = "",
    val metric: String = "", // load1|load5|load15|iowait|cpu|mem|swap|disk (+load alias)
    val threshold: Double = 0.0, // load: 0 = cores×2
    @SerialName("window_sec") val windowSec: Int = 0,
    val enabled: Boolean = true,
    @SerialName("last_notified") val lastNotified: Long = 0,
    @SerialName("breach_since") val breachSince: Long = 0,
)

@Serializable
data class HealthMetricDef(
    val key: String = "",
    val label: String = "",
    val unit: String = "",
    val chart: String = "",
)

@Serializable
data class HealthAlertDef(
    val metric: String = "",
    val threshold: Double = 0.0,
    @SerialName("window_sec") val windowSec: Int = 0,
)

@Serializable
data class HealthTemplate(
    val enabled: List<String> = emptyList(),
    val alerts: List<HealthAlertDef> = emptyList(),
)

@Serializable
data class HealthTemplateResponse(
    val template: HealthTemplate = HealthTemplate(),
    val catalog: List<HealthMetricDef> = emptyList(),
    val default: HealthTemplate = HealthTemplate(),
)

// --- tunnels/service.go ---

@Serializable
data class CfNodeRef(
    val id: String = "",
    val name: String = "",
)

// tunnels/service.go: tunnelOut
@Serializable
data class TunnelView(
    val id: String = "",
    val name: String = "",
    val status: String = "", // CF-side: healthy/degraded/down/inactive
    val node: CfNodeRef? = null,
    val process: String = "", // node systemd state: active/inactive/failed/activating
    val version: String = "",
    val online: Boolean = false,
    val managed: Boolean = false, // panel-created → start/stop/delete enabled
)

@Serializable
data class TunnelsResponse(
    val tunnels: List<TunnelView> = emptyList(),
)

@Serializable
data class CreateTunnelRequest(
    @SerialName("node_id") val nodeId: String,
    val name: String,
)

@Serializable
data class RenameTunnelRequest(
    val name: String,
)

// Loose envelope for the various {"id","action","note","deleted","moved",...}
// mutation responses; failures come back as non-2xx {"error": msg}.
@Serializable
data class CfActionResponse(
    val id: String = "",
    val name: String = "",
    val action: String = "",
    val note: String = "",
    val status: String = "",
    val deleted: Boolean = false,
    val moved: Boolean = false,
)

// --- domains/service.go ---

@Serializable
data class DomainRuleDns(
    val target: String = "", // CNAME target, "" when no record
    val proxied: Boolean = false,
    val matches: Boolean = false, // target == this tunnel's cfargotunnel.com
)

@Serializable
data class DomainRule(
    val hostname: String = "",
    val path: String = "",
    val service: String = "",
    @SerialName("is_catch_all") val isCatchAll: Boolean = false,
    val dns: DomainRuleDns? = null,
)

@Serializable
data class DomainTunnelView(
    val id: String = "",
    val name: String = "",
    val status: String = "",
    val node: CfNodeRef? = null,
    @SerialName("cname_target") val cnameTarget: String = "",
    val error: String = "",
    val rules: List<DomainRule> = emptyList(),
)

@Serializable
data class DomainsResponse(
    @SerialName("account_id") val accountId: String = "",
    val tunnels: List<DomainTunnelView> = emptyList(),
)

// AddRule POST /api/domains/rule {tunnel_id, hostname, path?, service}
@Serializable
data class DomainRuleRequest(
    @SerialName("tunnel_id") val tunnelId: String,
    val hostname: String,
    val path: String = "",
    val service: String,
)

// EditRule PUT /api/domains/rule — rule located by orig hostname(+path).
@Serializable
data class DomainRuleEditRequest(
    @SerialName("tunnel_id") val tunnelId: String,
    @SerialName("orig_hostname") val origHostname: String,
    @SerialName("orig_path") val origPath: String = "",
    val hostname: String,
    val path: String = "",
    val service: String,
)

// Move POST /api/domains/move {hostname, from_tunnel, to_tunnel, service}
@Serializable
data class DomainMoveRequest(
    val hostname: String,
    @SerialName("from_tunnel") val fromTunnel: String,
    @SerialName("to_tunnel") val toTunnel: String,
    val service: String,
)

// --- dns/service.go ---

@Serializable
data class DnsZone(
    val id: String = "",
    val name: String = "",
    val status: String = "",
)

@Serializable
data class DnsZonesResponse(
    val zones: List<DnsZone> = emptyList(),
)

// cloudflare.Record — flat shape shared by list/create/update
@Serializable
data class DnsRecord(
    val id: String = "",
    val type: String = "",
    val name: String = "",
    val content: String = "",
    val ttl: Int = 1, // 1 == Auto
    val proxied: Boolean = false,
    val priority: Int? = null, // MX/SRV only
    val comment: String = "",
)

@Serializable
data class DnsRecordsResponse(
    val records: List<DnsRecord> = emptyList(),
)

// dns/service.go: recordReq — zone_id required on create; update passes it as
// a query param and ignores the body field.
@Serializable
data class DnsRecordRequest(
    @SerialName("zone_id") val zoneId: String = "",
    val type: String,
    val name: String,
    val content: String,
    val ttl: Int = 1,
    val proxied: Boolean = false,
    val priority: Int? = null,
    val comment: String = "",
)

// store.Backup — one backup archive record
@Serializable
data class BackupView(
    val id: String = "",
    @SerialName("node_id") val nodeId: String = "",
    val name: String = "",
    val paths: String = "", // comma-joined, empty for container backups
    val container: String = "",
    @SerialName("container_name") val containerName: String = "",
    val size: Long = 0,
    val target: String = "",
    val status: String = "", // ok | failed | running
    val error: String = "",
    @SerialName("created_at") val createdAt: Long = 0,
)

@Serializable
data class IdResponse(
    val id: String = "",
)

// backup/service.go: BackupNow {node_id, paths, container, target_id, target_ids, name}
@Serializable
data class BackupNowRequest(
    @SerialName("node_id") val nodeId: String,
    val paths: List<String> = emptyList(),
    val container: String = "",
    @SerialName("target_ids") val targetIds: List<String> = emptyList(),
    val name: String = "",
)

// backup/service.go: Restore {node_id, dest}
@Serializable
data class RestoreRequest(
    @SerialName("node_id") val nodeId: String,
    val dest: String,
)

// backup/service.go: ListRestoreJobs — store.RestoreJob + node names
@Serializable
data class RestoreJobView(
    val id: String = "",
    @SerialName("backup_id") val backupId: String = "",
    val container: String = "",
    val image: String = "",
    @SerialName("origin_node") val originNode: String = "",
    @SerialName("target_node") val targetNode: String = "",
    val status: String = "", // running | ok | partial | failed
    val stage: String = "",
    val detail: String = "",
    val error: String = "",
    val percent: Long = 0,
    @SerialName("started_at") val startedAt: Long = 0,
    @SerialName("finished_at") val finishedAt: Long = 0,
    @SerialName("target_node_name") val targetNodeName: String = "",
    @SerialName("origin_node_name") val originNodeName: String = "",
)

// store.Schedule — Config is a JSON string; ScheduleRequest sends it as raw JSON.
@Serializable
data class ScheduleView(
    val id: String = "",
    val type: String = "", // backup | container_update
    @SerialName("node_id") val nodeId: String = "",
    val config: String = "",
    val cron: String = "",
    val enabled: Boolean = false,
    @SerialName("last_run") val lastRun: Long = 0,
    @SerialName("next_run") val nextRun: Long = 0,
    @SerialName("created_at") val createdAt: Long = 0,
)

// settings/service.go: Create/UpdateSchedule — cron built server-side from
// days+hour+minute when cron is empty; config is json.RawMessage.
@Serializable
data class ScheduleRequest(
    val type: String,
    @SerialName("node_id") val nodeId: String = "",
    val config: JsonObject = JsonObject(emptyMap()),
    val days: List<Int> = emptyList(),
    val hour: Int = 0,
    val minute: Int = 0,
    val cron: String = "",
    val enabled: Boolean = true,
)

// store.BackupTarget — config is a JSON string whose shape depends on type.
@Serializable
data class TargetView(
    val id: String = "",
    val type: String = "", // github | onedrive | vps | s3
    val name: String = "",
    val config: String = "",
    val enabled: Boolean = false,
)

// settings/service.go: Create/UpdateTarget — config sent as raw JSON.
@Serializable
data class TargetRequest(
    val type: String,
    val name: String,
    val config: JsonObject = JsonObject(emptyMap()),
    val enabled: Boolean = true,
)

// --- settings/service.go request/response shapes ---

@Serializable
data class AccountRequest(
    val username: String,
    @SerialName("new_password") val newPassword: String = "",
)

@Serializable
data class TelegramConfigRequest(
    @SerialName("bot_token") val botToken: String,
    @SerialName("chat_id") val chatId: String,
)

@Serializable
data class RetentionRequest(
    @SerialName("keep_count") val keepCount: Int,
    @SerialName("keep_days") val keepDays: Int,
)

@Serializable
data class ExcludesRequest(
    val excludes: List<String>,
)

@Serializable
data class DomainRequest(
    @SerialName("public_url") val publicUrl: String,
)

@Serializable
data class CloudflareConfigRequest(
    @SerialName("api_token") val apiToken: String = "",
)

@Serializable
data class CloudflareTestResponse(
    @SerialName("account_id") val accountId: String = "",
    val count: Int = 0,
)

@Serializable
data class ContainerMonitorRequest(
    val enabled: Boolean,
    @SerialName("interval_seconds") val intervalSeconds: Int,
)

@Serializable
data class KomariConfigRequest(
    @SerialName("base_url") val baseUrl: String,
    @SerialName("api_key") val apiKey: String,
    @SerialName("install_url") val installUrl: String = "",
)

@Serializable
data class KomariTestRequest(
    @SerialName("base_url") val baseUrl: String = "",
    @SerialName("api_key") val apiKey: String = "",
)

@Serializable
data class KomariTestResponse(
    val count: Int = 0,
)

// container/service.go: Update POST /api/container/update {node_id, label} — legacy "update all".
@Serializable
data class ContainerUpdateRequest(
    @SerialName("node_id") val nodeId: String,
    val label: String = "",
)

/** Extracts the backend {"error": msg} text from a non-2xx response. */
fun Response<*>.backendError(json: Json): String {
    val body = errorBody()?.string() ?: return "请求失败 (HTTP ${code()})"
    return runCatching { json.decodeFromString<ErrorResponse>(body).error }
        .getOrNull()
        ?.takeIf { it.isNotBlank() }
        ?: "请求失败 (HTTP ${code()})"
}
