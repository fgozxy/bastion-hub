package de.voilde.nodepanel.ui.update

import android.widget.Toast
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.BuildConfig
import de.voilde.nodepanel.data.update.VersionInfo
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import java.io.File

data class UpdateUiState(
    val currentVersionName: String = BuildConfig.VERSION_NAME,
    val available: VersionInfo? = null,
    val dismissed: Boolean = false,
    val downloading: Boolean = false,
    val progress: Int = 0,
    val downloaded: File? = null,
)

class UpdateViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(UpdateUiState())
    val uiState: StateFlow<UpdateUiState> = _uiState

    /** Startup check: never disturbs the user on failure. */
    fun silentCheck() {
        viewModelScope.launch {
            val info = container.updateManager.check() ?: return@launch
            if (info.versionCode > BuildConfig.VERSION_CODE) {
                _uiState.update { it.copy(available = info) }
            }
        }
    }

    /** Manual check: reports "already latest" / failure via toast. */
    fun checkNow() {
        viewModelScope.launch {
            val info = container.updateManager.check()
            when {
                info == null -> toast("检查更新失败")
                info.versionCode > BuildConfig.VERSION_CODE ->
                    _uiState.update { it.copy(available = info, dismissed = false) }
                else -> toast("已是最新版本（${BuildConfig.VERSION_NAME}）")
            }
        }
    }

    fun dismiss() = _uiState.update { it.copy(dismissed = true) }

    fun downloadAndInstall() {
        val info = _uiState.value.available ?: return
        if (_uiState.value.downloading) return
        _uiState.update { it.copy(downloading = true, progress = 0) }
        viewModelScope.launch {
            try {
                val file = container.updateManager.download(info) { pct ->
                    _uiState.update { it.copy(progress = pct) }
                }
                _uiState.update { it.copy(downloading = false, downloaded = file) }
                install(file)
            } catch (e: Exception) {
                _uiState.update { it.copy(downloading = false) }
                toast("下载失败：${e.message ?: e.javaClass.simpleName}")
            }
        }
    }

    fun install(file: File) {
        runCatching { container.app.startActivity(container.updateManager.installIntent(file)) }
            .onFailure { toast("无法打开安装器") }
    }

    private fun toast(msg: String) {
        Toast.makeText(container.app, msg, Toast.LENGTH_SHORT).show()
    }
}
