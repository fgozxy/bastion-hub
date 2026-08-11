package de.voilde.nodepanel.data.api

import com.jakewharton.retrofit2.converter.kotlinx.serialization.asConverterFactory
import de.voilde.nodepanel.data.SessionManager
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import retrofit2.Retrofit
import java.util.concurrent.TimeUnit

// Builds Retrofit against the currently configured base URL; recreated when
// the user changes the server address on the login screen.
class ApiClient(private val sessionManager: SessionManager) {

    val json = Json {
        ignoreUnknownKeys = true
        coerceInputValues = true
    }

    private val okHttp = OkHttpClient.Builder()
        .cookieJar(sessionManager.cookieJar)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .build()

    // Long-running synchronous ops (tunnel create installs cloudflared on the
    // node: 180s + 60s ExecSync ceiling) need a read timeout beyond the 30s
    // default. Targeted second client instead of raising the global one, so a
    // hung regular endpoint still fails fast. Used only via apiLong().
    private val okHttpLong = okHttp.newBuilder()
        .readTimeout(300, TimeUnit.SECONDS)
        .build()

    @Volatile
    private var cachedBaseUrl: String? = null

    @Volatile
    private var cachedApi: NodePanelApi? = null

    @Volatile
    private var cachedApiLong: NodePanelApi? = null

    fun api(): NodePanelApi = buildApi(okHttp) { cachedApi }.also { cachedApi = it }

    fun apiLong(): NodePanelApi = buildApi(okHttpLong) { cachedApiLong }.also { cachedApiLong = it }

    private fun buildApi(client: OkHttpClient, cache: () -> NodePanelApi?): NodePanelApi {
        val baseUrl = sessionManager.baseUrl
        val cached = cache()
        if (cached != null && cachedBaseUrl == baseUrl) return cached
        return Retrofit.Builder()
            .baseUrl("$baseUrl/")
            .client(client)
            .addConverterFactory(json.asConverterFactory("application/json".toMediaType()))
            .build()
            .create(NodePanelApi::class.java)
            .also { cachedBaseUrl = baseUrl }
    }

    // Browser websocket (/api/ws). The session cookie is sent explicitly — the
    // CookieJar is not guaranteed to apply on the WS upgrade path.
    fun newBrowserWebSocket(listener: WebSocketListener): WebSocket {
        val wsUrl = sessionManager.baseUrl.replaceFirst("http", "ws") + "/api/ws"
        val builder = Request.Builder().url(wsUrl)
        sessionManager.sessionCookieHeader()?.let { builder.header("Cookie", it) }
        // No read timeout on the socket: the server pings every 30s to keep alive.
        return okHttp.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).build()
            .newWebSocket(builder.build(), listener)
    }
}
