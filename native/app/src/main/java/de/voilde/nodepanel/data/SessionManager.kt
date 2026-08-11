package de.voilde.nodepanel.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl

private val Context.dataStore by preferencesDataStore(name = "session")

/**
 * Holds the panel base URL and the np_session cookie.
 * Two layers: an in-memory copy for synchronous OkHttp access, persisted to
 * DataStore so the session survives process death (no re-login on cold start).
 */
class SessionManager(private val context: Context) {

    companion object {
        const val DEFAULT_BASE_URL = "https://panel.voilde.de"
        const val COOKIE_NAME = "np_session"
        private val KEY_BASE_URL = stringPreferencesKey("base_url")
        private val KEY_COOKIE = stringPreferencesKey("session_cookie")
    }

    @Volatile
    private var cookieValue: String? = null

    @Volatile
    var baseUrl: String = DEFAULT_BASE_URL
        private set

    init {
        // One blocking read at startup so routing decisions can be synchronous.
        runBlocking {
            val prefs = context.dataStore.data.first()
            baseUrl = prefs[KEY_BASE_URL] ?: DEFAULT_BASE_URL
            cookieValue = prefs[KEY_COOKIE]
        }
    }

    fun hasSession(): Boolean = !cookieValue.isNullOrBlank()

    /** "np_session=<value>" for hand-built requests (e.g. the browser WS). */
    fun sessionCookieHeader(): String? =
        cookieValue?.takeIf { it.isNotBlank() }?.let { "$COOKIE_NAME=$it" }

    suspend fun setBaseUrl(url: String) {
        baseUrl = url.trimEnd('/')
        context.dataStore.edit { it[KEY_BASE_URL] = baseUrl }
    }

    fun saveSession(value: String) {
        cookieValue = value
        runBlocking { context.dataStore.edit { it[KEY_COOKIE] = value } }
    }

    fun clearSession() {
        cookieValue = null
        runBlocking { context.dataStore.edit { it.remove(KEY_COOKIE) } }
    }

    /** OkHttp CookieJar bridging the in-memory session cookie. */
    val cookieJar = object : CookieJar {
        override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
            cookies.firstOrNull { it.name == COOKIE_NAME }?.let { saveSession(it.value) }
        }

        override fun loadForRequest(url: HttpUrl): List<Cookie> {
            val value = cookieValue ?: return emptyList()
            val cookie = Cookie.Builder()
                .name(COOKIE_NAME)
                .value(value)
                .domain(url.host)
                .path("/")
                .apply { if (url.isHttps) secure() }
                .build()
            return listOf(cookie)
        }
    }
}
