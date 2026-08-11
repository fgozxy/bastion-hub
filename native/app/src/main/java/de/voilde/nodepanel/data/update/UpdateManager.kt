package de.voilde.nodepanel.data.update

import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import de.voilde.nodepanel.data.SessionManager
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.json.Json
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.File
import java.util.concurrent.TimeUnit

/** Entry in {baseUrl}/downloads/version.json, maintained at publish time. */
@Serializable
data class VersionInfo(
    val versionCode: Int = 0,
    val versionName: String = "",
    val url: String = "",
    val notes: String = "",
)

// Checks and downloads APK updates from the panel's /downloads/ directory.
class UpdateManager(
    private val context: Context,
    private val sessionManager: SessionManager,
    private val json: Json,
) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.MINUTES) // APK downloads can be slow
        .build()

    /** Returns null when version.json is missing/unparseable (silent check). */
    suspend fun check(): VersionInfo? = withContext(Dispatchers.IO) {
        runCatching {
            client.newCall(
                Request.Builder()
                    // Cache-buster: Cloudflare may cache version.json, so always
                    // ask for a fresh copy.
                    .url("${sessionManager.baseUrl}/downloads/version.json?ts=${System.currentTimeMillis()}")
                    .build(),
            ).execute().use { resp ->
                if (!resp.isSuccessful) return@use null
                val body = resp.body?.string() ?: return@use null
                // The SPA fallback serves index.html with HTTP 200 for unknown
                // paths, so an unparseable body just means "no update info".
                runCatching { json.decodeFromString<VersionInfo>(body) }.getOrNull()
            }
        }.getOrNull()
    }

    fun resolveUrl(info: VersionInfo): String =
        if (info.url.startsWith("http")) info.url else sessionManager.baseUrl + info.url

    suspend fun download(info: VersionInfo, onProgress: (percent: Int) -> Unit): File =
        withContext(Dispatchers.IO) {
            val dir = File(context.getExternalFilesDir(null), "updates").apply { mkdirs() }
            val out = File(dir, "NodePanel-${info.versionName}.apk")
            client.newCall(Request.Builder().url(resolveUrl(info)).build()).execute().use { resp ->
                if (!resp.isSuccessful) error("下载失败 (HTTP ${resp.code})")
                val body = resp.body ?: error("下载失败：空响应")
                val total = body.contentLength()
                body.byteStream().use { input ->
                    out.outputStream().use { output ->
                        val buf = ByteArray(64 * 1024)
                        var done = 0L
                        var lastPct = -1
                        while (true) {
                            val n = input.read(buf)
                            if (n < 0) break
                            output.write(buf, 0, n)
                            done += n
                            if (total > 0) {
                                val pct = (done * 100 / total).toInt()
                                if (pct != lastPct) {
                                    lastPct = pct
                                    onProgress(pct)
                                }
                            }
                        }
                    }
                }
            }
            out
        }

    fun installIntent(file: File): Intent {
        val uri = FileProvider.getUriForFile(context, "de.voilde.nodepanel.fileprovider", file)
        return Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
    }
}
