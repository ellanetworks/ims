import javax.inject.Inject

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.roborazzi)
    alias(libs.plugins.spotless)
}

val goModule: Directory = layout.settingsDirectory.dir("..")

val release: String = providers.fileContents(goModule.file("version/VERSION")).asText.get().trim().let {
    requireNotNull(Regex("""v(\d+\.\d+\.\d+)""").matchEntire(it)) { "version/VERSION: $it, want vX.Y.Z" }.groupValues[1]
}

val build: Int = providers.gradleProperty("mcptt.build").map(String::toInt).getOrElse(1)

fun secret(name: String): String = providers.environmentVariable(name).orNull?.takeIf(String::isNotEmpty) ?: error("$name is not set")

android {
    namespace = "com.ellanetworks.mcptt"
    compileSdk = 37
    ndkVersion = "28.2.13676358"

    defaultConfig {
        applicationId = "com.ellanetworks.mcptt"
        minSdk = 34
        targetSdk = 36
        versionCode = build
        versionName = if (build == 1) release else "$release+$build"

        ndk { abiFilters += "arm64-v8a" }
    }

    signingConfigs {
        providers.environmentVariable("MCPTT_KEYSTORE").orNull?.let { keystore ->
            create("release") {
                storeFile = file(keystore)
                storePassword = secret("MCPTT_KEYSTORE_PASSWORD")
                keyAlias = secret("MCPTT_KEY_ALIAS")
                keyPassword = secret("MCPTT_KEY_PASSWORD")
            }
        }

        providers.gradleProperty("mcptt.devKeystore").orNull?.let { keystore ->
            named("debug") {
                storeFile = file(keystore)
                storePassword = providers.gradleProperty("mcptt.devKeystorePassword").get()
                keyAlias = providers.gradleProperty("mcptt.devKeyAlias").get()
                keyPassword = providers.gradleProperty("mcptt.devKeyPassword").get()
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"))
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        buildConfig = true
        compose = true
    }

    lint {
        warningsAsErrors = true
        abortOnError = true
        disable += setOf(
            "AndroidGradlePluginVersion",
            "GradleDependency",
            "NewerVersionAvailable",
            "OldTargetApi",
            "ChromeOsAbiSupport",
        )
    }

    testOptions {
        unitTests.isIncludeAndroidResources = true
        unitTests.all {
            val contract = goModule.dir("mcptt/mobile/testdata/contract")
            it.inputs.dir(contract).withPathSensitivity(PathSensitivity.RELATIVE)
            it.systemProperty("mcptt.contract", contract.asFile.path)
        }
    }
}

val ktlintRules = mapOf(
    "ktlint_code_style" to "intellij_idea",
    "max_line_length" to "160",
    "ij_kotlin_allow_trailing_comma" to "true",
    "ij_kotlin_allow_trailing_comma_on_call_site" to "true",
    "ktlint_function_naming_ignore_when_annotated_with" to "Composable",
    "compose_allowed_composition_locals" to "LocalEllaColors",
    "ktlint_compose_vm-forwarding-check" to "disabled",
)

spotless {
    kotlin {
        target("src/**/*.kt")
        ktlint(libs.versions.ktlint.get())
            .editorConfigOverride(ktlintRules)
            .customRuleSets(listOf(libs.compose.rules.ktlint.get().toString()))
    }
    kotlinGradle {
        target("*.gradle.kts")
        ktlint(libs.versions.ktlint.get()).editorConfigOverride(ktlintRules)
    }
}

roborazzi {
    outputDir = layout.projectDirectory.dir("src/test/screenshots")
}

abstract class GomobileBind @Inject constructor(private val exec: ExecOperations) : DefaultTask() {
    @get:InputFiles
    @get:PathSensitive(PathSensitivity.RELATIVE)
    abstract val sources: ConfigurableFileCollection

    @get:Internal
    abstract val module: DirectoryProperty

    @get:Input
    abstract val goVersion: Property<String>

    @get:Input
    abstract val pkg: Property<String>

    @get:Input
    abstract val javaPkg: Property<String>

    @get:Input
    abstract val androidApi: Property<Int>

    @get:Internal
    abstract val sdk: DirectoryProperty

    @get:Internal
    abstract val ndk: DirectoryProperty

    @get:Input
    abstract val ndkVersion: Property<String>

    @get:OutputDirectory
    abstract val out: DirectoryProperty

    @get:Internal
    val aar: Provider<RegularFile>
        get() = out.file("mobile.aar")

    @TaskAction
    fun bind() {
        val bin = temporaryDir
        val dir = module.get().asFile
        val env = mapOf("GOWORK" to "off", "GOFLAGS" to "-mod=readonly")

        exec.exec {
            workingDir = dir
            environment(env)
            commandLine("go", "build", "-o", "$bin/", "golang.org/x/mobile/cmd/gomobile", "golang.org/x/mobile/cmd/gobind")
        }

        exec.exec {
            workingDir = dir
            environment(env)
            val jdk = File(System.getProperty("java.home"), "bin")
            environment("PATH", listOf(bin, jdk, System.getenv("PATH")).joinToString(File.pathSeparator))
            environment("ANDROID_HOME", sdk.get().asFile.path)
            environment("ANDROID_NDK_HOME", ndk.get().asFile.path)
            commandLine(
                "$bin/gomobile", "bind", "-target", "android/arm64", "-androidapi", androidApi.get().toString(),
                "-javapkg", javaPkg.get(), "-trimpath",
                "-ldflags", "-s -w -extldflags=-Wl,-z,max-page-size=16384",
                "-o", aar.get().asFile.path, pkg.get(),
            )
        }
    }
}

val bindMobile = tasks.register<GomobileBind>("bindMobile") {
    description = "Binds the Go session (mobile) to an AAR."
    module = goModule
    sources.from(goModule.files("go.mod", "go.sum", "sip/go.mod", "sip/go.sum"))
    for (d in listOf("mcptt", "sip", "ue", "internal")) {
        sources.from(goModule.dir(d).asFileTree.matching { exclude("**/*_test.go", "**/testdata/**") })
    }
    goVersion = providers.exec { commandLine("go", "env", "GOVERSION") }.standardOutput.asText.map(String::trim)
    pkg = "./mcptt/mobile"
    javaPkg = "com.ellanetworks.mcptt.go"
    androidApi = android.defaultConfig.minSdk
    sdk = androidComponents.sdkComponents.sdkDirectory
    ndk = androidComponents.sdkComponents.ndkDirectory
    ndkVersion = android.ndkVersion
    out = layout.buildDirectory.dir("mobile")
}

dependencies {
    implementation(files(bindMobile.flatMap { it.aar }))

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.compose.material.icons.extended)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.core.telecom)
    implementation(libs.androidx.datastore)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)
    debugImplementation(libs.androidx.compose.ui.tooling)

    testImplementation(libs.junit)
    testImplementation(libs.kotlinx.coroutines.test)
    testImplementation(platform(libs.androidx.compose.bom))
    testImplementation(libs.androidx.compose.ui.test.junit4)
    testImplementation(libs.robolectric)
    testImplementation(libs.roborazzi)
    testImplementation(libs.roborazzi.compose)
    debugImplementation(libs.androidx.compose.ui.test.manifest)
}
