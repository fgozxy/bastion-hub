# NodePanel Android App

独立 Android 客户端（Capacitor WebView 壳），连接面板 Web 界面。

## 功能

与网页版同一面板：节点、容器、备份 / 恢复 / 迁移、域名 / 隧道 / DNS、健康监控、命令执行等。安装后自动拉最新前端，无需为每次 UI 改动重装 APK。

## 已构建 APK（Debug）

```
android/app/build/outputs/apk/debug/app-debug.apk
```

Debug 签名，**仅供内测安装**（需允许「未知来源」）。

发布到面板（拷贝进 `web/public/downloads/` 后重建容器）：

```bash
npm run dist:apk
cd /path/to/nodepanel && docker compose up -d --build
```

## 环境

```bash
export JAVA_HOME=/usr/lib/jvm/java-21-openjdk-amd64
export ANDROID_HOME=/opt/android-sdk
export ANDROID_SDK_ROOT=$ANDROID_HOME
export PATH=$PATH:$ANDROID_HOME/platform-tools
```

## 重新打包

```bash
cd mobile
npx cap sync android
cd android && ./gradlew assembleDebug
```

或一键：

```bash
npm run build:debug
```

Release（需自备 keystore）：

```bash
cd android && ./gradlew assembleRelease
```

## 配置

`capacitor.config.json` 中 `server.url` 指向你的面板域名：

```json
"server": { "url": "https://panel.example.com" }
```

改域名后执行 `npx cap sync android` 再重新 `assembleDebug`。

## 说明

- 壳模式：UI 实时跟网站走，不必为每次前端改动重装 APK
- 若要离线壳 + 本地资源，把 `web/dist/` 拷到 `www/`，去掉 `server.url`
