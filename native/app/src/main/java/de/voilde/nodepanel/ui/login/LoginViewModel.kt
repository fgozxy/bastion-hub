package de.voilde.nodepanel.ui.login

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.SessionManager
import de.voilde.nodepanel.data.api.ErrorResponse
import de.voilde.nodepanel.data.api.LoginRequest
import kotlinx.serialization.decodeFromString
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class LoginUiState(
    val serverUrl: String = SessionManager.DEFAULT_BASE_URL,
    val username: String = "",
    val password: String = "",
    val loading: Boolean = false,
    val error: String? = null,
)

class LoginViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(LoginUiState(serverUrl = container.sessionManager.baseUrl))
    val uiState: StateFlow<LoginUiState> = _uiState

    fun onServerUrlChange(value: String) = _uiState.update { it.copy(serverUrl = value, error = null) }
    fun onUsernameChange(value: String) = _uiState.update { it.copy(username = value, error = null) }
    fun onPasswordChange(value: String) = _uiState.update { it.copy(password = value, error = null) }

    fun login(onSuccess: () -> Unit) {
        val state = _uiState.value
        if (state.loading) return
        if (state.serverUrl.isBlank() || state.username.isBlank() || state.password.isBlank()) {
            _uiState.update { it.copy(error = "请填写服务器地址、用户名和密码") }
            return
        }
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                container.sessionManager.setBaseUrl(state.serverUrl.trim())
                val response = container.apiClient.api()
                    .login(LoginRequest(state.username.trim(), state.password))
                if (response.isSuccessful) {
                    onSuccess()
                } else {
                    val message = response.errorBody()?.string()
                        ?.let { body ->
                            runCatching {
                                container.apiClient.json.decodeFromString<ErrorResponse>(body).error
                            }.getOrNull()
                        }
                        ?: "登录失败 (HTTP ${response.code()})"
                    _uiState.update { it.copy(error = message) }
                }
            } catch (e: Exception) {
                _uiState.update { it.copy(error = "无法连接服务器：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                _uiState.update { it.copy(loading = false) }
            }
        }
    }
}
