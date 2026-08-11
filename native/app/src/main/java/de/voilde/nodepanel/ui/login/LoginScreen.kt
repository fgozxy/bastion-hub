package de.voilde.nodepanel.ui.login

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel
import de.voilde.nodepanel.AppContainer
import de.voilde.nodepanel.ui.components.LogoDot
import de.voilde.nodepanel.ui.components.NpButton
import de.voilde.nodepanel.ui.theme.NpTheme

@Composable
fun LoginScreen(
    container: AppContainer,
    onLoginSuccess: () -> Unit,
    viewModel: LoginViewModel = viewModel { LoginViewModel(container) },
) {
    val state by viewModel.uiState.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        LogoDot(size = 56.dp)
        Spacer(Modifier.height(12.dp))
        Text(
            "NodePanel",
            fontSize = 22.sp,
            fontWeight = FontWeight.SemiBold,
            color = NpTheme.colors.textPrimary,
        )
        Text(
            "登录你的自托管面板",
            style = MaterialTheme.typography.bodySmall,
            color = NpTheme.colors.textTertiary,
        )
        Spacer(Modifier.height(28.dp))

        val fieldColors = OutlinedTextFieldDefaults.colors(
            focusedContainerColor = NpTheme.colors.bgInput,
            unfocusedContainerColor = NpTheme.colors.bgInput,
            focusedBorderColor = NpTheme.colors.primary,
            unfocusedBorderColor = NpTheme.colors.borderStrong,
        )
        OutlinedTextField(
            value = state.serverUrl,
            onValueChange = viewModel::onServerUrlChange,
            label = { Text("服务器地址") },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(12.dp))
        OutlinedTextField(
            value = state.username,
            onValueChange = viewModel::onUsernameChange,
            label = { Text("用户名") },
            singleLine = true,
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(12.dp))
        OutlinedTextField(
            value = state.password,
            onValueChange = viewModel::onPasswordChange,
            label = { Text("密码") },
            singleLine = true,
            visualTransformation = PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
            colors = fieldColors,
            modifier = Modifier.fillMaxWidth(),
        )

        state.error?.let {
            Spacer(Modifier.height(12.dp))
            Text(it, color = NpTheme.colors.warning, style = MaterialTheme.typography.bodyMedium)
        }

        Spacer(Modifier.height(24.dp))
        NpButton(
            text = "登录",
            onClick = { viewModel.login(onLoginSuccess) },
            enabled = !state.loading,
            loading = state.loading,
            modifier = Modifier.fillMaxWidth(),
        )
    }
}
