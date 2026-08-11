# NodePanel release ProGuard rules.

# ============================================================================
# Generic signatures — CRITICAL. Retrofit + the kotlinx-serialization converter
# reflect on ParameterizedType (Response<Foo>, List<Foo>); if R8 strips the
# Signature attribute the app dies with
# "java.lang.Class cannot be cast to java.lang.reflect.ParameterizedType".
# InnerClasses is required to use Signature, EnclosingMethod for InnerClasses.
# ============================================================================
-keepattributes Signature, *Annotation*, InnerClasses, EnclosingMethod, Exceptions
# Retrofit reads method/parameter annotations at runtime.
-keepattributes RuntimeVisibleAnnotations, RuntimeVisibleParameterAnnotations
# Annotation default values (e.g. retrofit2.http.Field.encoded).
-keepattributes AnnotationDefault

# --- Retrofit (official rules, retrofit2 GitHub README) ---
# Retain service method signatures (and their generic return types) on any
# interface carrying Retrofit HTTP annotations; obfuscation of names is fine.
-keepclassmembers,allowshrinking,allowobfuscation interface * {
    @retrofit2.http.* <methods>;
}
# Ignore annotation used for build tooling.
-dontwarn org.codehaus.mojo.animal_sniffer.IgnoreJRERequirement
# Guarded by a NoClassDefFoundError try/catch and only used when on the classpath.
-dontwarn kotlin.Unit
# Top-level functions that can only be used by Kotlin.
-dontwarn retrofit2.KotlinExtensions
-dontwarn retrofit2.KotlinExtensions$*
-dontwarn retrofit2.**

# --- OkHttp (official rules) ---
-dontwarn okhttp3.**
-dontwarn javax.annotation.**
# A resource is loaded with a relative path so its package must be preserved.
-adaptresourcefilenames okhttp3/internal/publicsuffix/PublicSuffixDatabase.gz
# Animal Sniffer compileOnly dependency for older-Java API compatibility.
-dontwarn org.codehaus.mojo.animal_sniffer.*
# OkHttp platform used only on JVM / with optional security providers.
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**

# --- Okio ---
-dontwarn okio.**

# --- kotlinx.serialization (official rules) ---
-dontnote kotlinx.serialization.AnnotationsKt

# kotlinx-serialization-json specific: JsonObjectSerializer et al. are looked
# up reflectively via the Companion on the Json* classes.
-keepclassmembers class kotlinx.serialization.json.** {
    *** Companion;
}
-keepclasseswithmembers class kotlinx.serialization.json.** {
    kotlinx.serialization.KSerializer serializer(...);
}

# Generated serializers for this app's @Serializable DTOs.
-keep,includedescriptorclasses class de.voilde.nodepanel.**$$serializer { *; }
-keepclassmembers class de.voilde.nodepanel.** {
    *** Companion;
}

# --- App DTOs + Retrofit service interface ---
# Kept wholesale: the surface is small, field names ARE the JSON wire format
# (snake_case via @SerialName), and the interface methods carry the generic
# return types Retrofit needs unrenamed.
-keep class de.voilde.nodepanel.data.api.** { *; }
