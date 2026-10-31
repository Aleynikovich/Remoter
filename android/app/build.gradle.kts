plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// CI passes the workflow run number, so every published APK has a higher
// versionCode than the one before and the in-app updater can compare them.
val buildNumber = (System.getenv("REMOTER_BUILD_NUMBER") ?: "0").toInt()

android {
    namespace = "io.github.aleynikovich.remoter"
    compileSdk = 35

    defaultConfig {
        applicationId = "io.github.aleynikovich.remoter"
        minSdk = 30
        targetSdk = 35
        versionCode = buildNumber
        versionName = if (buildNumber > 0) "android-dev-$buildNumber" else "local"
    }

    signingConfigs {
        // One key checked into the repo, so every build (local or CI) can
        // install over the previous one. It is a sideloading key, not a
        // store key.
        create("sideload") {
            storeFile = file("remoter-debug.keystore")
            storePassword = "android"
            keyAlias = "remoter"
            keyPassword = "android"
        }
    }

    buildTypes {
        debug {
            signingConfig = signingConfigs.getByName("sideload")
        }
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("sideload")
        }
    }

    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    testOptions {
        unitTests.isReturnDefaultValues = true
    }
}

dependencies {
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    testImplementation("junit:junit:4.13.2")
}
