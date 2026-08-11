#!/usr/bin/env bash
# NodePanel Android 构建环境（本机 limited server 用）
export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64
export ANDROID_HOME=/opt/android-sdk
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PATH="$PATH:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools"
# 低内存服务器：限制 Gradle JVM
export GRADLE_OPTS="-Xmx1536m -XX:MaxMetaspaceSize=512m -Dorg.gradle.daemon=false"
export _JAVA_OPTIONS="-Xmx1536m"
