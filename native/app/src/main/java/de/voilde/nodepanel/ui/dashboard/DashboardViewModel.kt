package de.voilde.nodepanel.ui.dashboard

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.data.api.DashboardStats
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

data class DashboardUiState(
    val loading: Boolean = true,
    val stats: DashboardStats? = null,
    // node id -> display name, used to label the per-node metric cards
    val nodeNames: Map<String, String> = emptyMap(),
    val error: String? = null,
)

class DashboardViewModel(private val container: AppContainer) : ViewModel() {

    private val _uiState = MutableStateFlow(DashboardUiState())
    val uiState: StateFlow<DashboardUiState> = _uiState

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(loading = true, error = null) }
            try {
                val (stats, names) = coroutineScope {
                    val statsDeferred = async { container.apiClient.api().dashboard() }
                    val namesDeferred = async {
                        runCatching { container.apiClient.api().nodes() }
                            .getOrDefault(emptyList())
                            .associate { it.id to it.name.ifBlank { it.hostname.ifBlank { it.id } } }
                    }
                    statsDeferred.await() to namesDeferred.await()
                }
                _uiState.update { it.copy(loading = false, stats = stats, nodeNames = names) }
            } catch (e: Exception) {
                _uiState.update { it.copy(loading = false, error = "加载失败：${e.message ?: e.javaClass.simpleName}") }
            }
        }
    }
}
