import javax.inject.Inject

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.roborazzi)
    alias(libs.plugins.spotless)
}

// The Go module of Ella IMS, whose mcptt/mobile package the app binds.
val goModule: Directory = layout.settingsDirectory.dir("..")

// The release of Ella IMS (version/VERSION, vX.Y.Z), the app's version name.
val release: String = providers.fileContents(goModule.file("version/VERSION")).asText.get().trim().let {
    requireNotNull(Regex("""v(\d+\.\d+\.\d+)""").matchEntire(it)) { "version/VERSION: $it, want vX.Y.Z" }.groupValues[1]
}

// The build of the release, CI's run number: the app's version code, so that each build of main updates the last
// (decision 2: the fleet updated by its MDM). 1 for a local build.
val build: Int = providers.gradleProperty("mcptt.build").map(String::toInt).getOrElse(1)

// A key of the environment, required when its keystore is given.
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

        // The four phones of the fleet are arm64 (decision 5).
        ndk { abiFilters += "arm64-v8a" }
    }

    signingConfigs {
        // The release key, whose certificate's SHA-1 every SIM's ARA-M rules grant carrier privileges (decision 7):
        // CI's secrets, on main only. Without it the release is unsigned.
        providers.environmentVariable("MCPTT_KEYSTORE").orNull?.let { keystore ->
            create("release") {
                storeFile = file(keystore)
                storePassword = secret("MCPTT_KEYSTORE_PASSWORD")
                keyAlias = secret("MCPTT_KEY_ALIAS")
                keyPassword = secret("MCPTT_KEY_PASSWORD")
            }
        }

        // The developers' key, in the lab SIMs' ARA-M rules only: of the Gradle properties (~/.gradle/gradle.properties).
        // Without it a debug build has the machine's debug key, and no carrier privileges.
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
        // Newer versions are Dependabot's to raise, not a failure of the build that changes with the day; ChromeOS has
        // no cellular IMS (decision 8).
        disable += setOf(
            "AndroidGradlePluginVersion",
            "GradleDependency",
            "NewerVersionAvailable",
            "OldTargetApi",
            "ChromeOsAbiSupport",
        )
    }

    testOptions {
        // Robolectric's: the app's resources, its fonts, strings and theme, in the screenshots.
        unitTests.isIncludeAndroidResources = true
        unitTests.all {
            // The JSON of the session, as the Go package mobile's tests keep it (mcptt/mobile/testdata/contract).
            val contract = goModule.dir("mcptt/mobile/testdata/contract")
            it.inputs.dir(contract).withPathSensitivity(PathSensitivity.RELATIVE)
            it.systemProperty("mcptt.contract", contract.asFile.path)
        }
    }
}

// The Kotlin's format and the Compose rules (ktlint): spotlessCheck in CI, spotlessApply to fix.
val ktlintRules = mapOf(
    // IntelliJ's Kotlin style, as kotlin.code.style=official, with the repo's line length.
    "ktlint_code_style" to "intellij_idea",
    "max_line_length" to "160",
    "ij_kotlin_allow_trailing_comma" to "true",
    "ij_kotlin_allow_trailing_comma_on_call_site" to "true",
    // Composables are named as types.
    "ktlint_function_naming_ignore_when_annotated_with" to "Composable",
    // The theme's extended colours, as MaterialTheme's (theme/Color.kt).
    "compose_allowed_composition_locals" to "LocalEllaColors",
    // The screens take the one ViewModel until their state is hoisted (android_review.md, engineering: architecture).
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

// The screenshots of the UI (Robolectric's native graphics), kept with the tests: verifyRoborazziDebug in CI compares
// them, recordRoborazziDebug records them anew.
roborazzi {
    outputDir = layout.projectDirectory.dir("src/test/screenshots")
}

// GomobileBind binds the Go package to an AAR, and its Java sources to a jar: gomobile and gobind of the module's
// go.mod, the SDK and NDK of the build. The module alone, whatever go.work is about.
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
            // gomobile compiles the bindings' Java with javac: the JDK running the build's.
            val jdk = File(System.getProperty("java.home"), "bin")
            environment("PATH", listOf(bin, jdk, System.getenv("PATH")).joinToString(File.pathSeparator))
            environment("ANDROID_HOME", sdk.get().asFile.path)
            environment("ANDROID_NDK_HOME", ndk.get().asFile.path)
            commandLine(
                "$bin/gomobile", "bind", "-target", "android/arm64", "-androidapi", androidApi.get().toString(),
                "-javapkg", javaPkg.get(), "-trimpath",
                // Stripped of the ELF symbols and DWARF; Go's tracebacks keep their names. 16 KB pages (Android 15).
                "-ldflags", "-s -w -extldflags=-Wl,-z,max-page-size=16384",
                "-o", aar.get().asFile.path, pkg.get(),
            )
        }
    }
}

val bindMobile = tasks.register<GomobileBind>("bindMobile") {
    description = "Binds the Go session (mobile) to an AAR."
    module = goModule
    // The packages mobile imports of the module, and their embedded files: all but tests.
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
