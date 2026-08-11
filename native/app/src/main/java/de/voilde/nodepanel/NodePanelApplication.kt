package de.voilde.nodepanel

import android.app.Application
import de.voilde.nodepanel.data.SessionManager
import de.voilde.nodepanel.data.api.ApiClient
import de.voilde.nodepanel.data.update.UpdateManager

// Manual DI container: no Hilt, dependencies are wired here once.
class AppContainer(val app: Application) {
    val sessionManager = SessionManager(app)
    val apiClient = ApiClient(sessionManager)
    val updateManager = UpdateManager(app, sessionManager, apiClient.json)
}

class NodePanelApplication : Application() {
    lateinit var container: AppContainer
        private set

    override fun onCreate() {
        super.onCreate()
        container = AppContainer(this)
    }
}
