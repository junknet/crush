const { withMainActivity } = require('@expo/config-plugins')
const packageJson = require('./package.json')

const IS_DEV = process.env.APP_VARIANT === 'development'
const ANDROID_VERSION_CODE = Number(process.env.CRUSH_MOBILE_ANDROID_VERSION_CODE || 1)

const withDarkAndroidSystemBars = (config) =>
    withMainActivity(config, (mod) => {
        if (mod.modResults.language !== 'kt') return mod

        let contents = mod.modResults.contents
        const imports = [
            'import android.graphics.Color',
            'import android.view.View',
            'import android.view.WindowInsetsController',
        ]
        for (const line of imports) {
            if (!contents.includes(line)) {
                contents = contents.replace(
                    'import expo.modules.splashscreen.SplashScreenManager\n',
                    `import expo.modules.splashscreen.SplashScreenManager\n\n${line}\n`
                )
            }
        }

        if (!contents.includes('    applyDarkSystemBars()\n')) {
            contents = contents.replace(
                '    super.onCreate(null)\n',
                '    super.onCreate(null)\n    applyDarkSystemBars()\n'
            )
        }

        if (!contents.includes('private fun applyDarkSystemBars()')) {
            const darkSystemBarsMethod = `
  override fun onResume() {
    super.onResume()
    applyDarkSystemBars()
  }

  @Suppress("DEPRECATION")
  private fun applyDarkSystemBars() {
    window.statusBarColor = Color.TRANSPARENT
    window.navigationBarColor = Color.parseColor("#0a0d14")

    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
      window.isNavigationBarContrastEnforced = false
      window.isStatusBarContrastEnforced = false
    }

    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
      window.insetsController?.setSystemBarsAppearance(
        0,
        WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS or
          WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
      )
    } else {
      window.decorView.systemUiVisibility =
        window.decorView.systemUiVisibility and
          View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR.inv() and
          View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR.inv()
    }
  }
`
            contents = contents.replace(
                '\n  /**\n   * Returns the name of the main component',
                `${darkSystemBarsMethod}\n  /**\n   * Returns the name of the main component`
            )
        }

        mod.modResults.contents = contents
        return mod
    })

module.exports = {
    expo: {
        name: IS_DEV ? 'Crush Mobile (DEV)' : 'Crush Mobile',
        slug: 'crush-mobile',
        version: packageJson.version,
        orientation: 'default',
        scheme: 'crushmobile',
        userInterfaceStyle: 'dark',
        icon: './assets/images/icon.png',
        splash: {
            image: './assets/images/splash.png',
            resizeMode: 'contain',
            backgroundColor: '#090b10',
        },
        ios: {
            supportsTablet: true,
            bundleIdentifier: IS_DEV ? 'com.junknet.crushmobile.dev' : 'com.junknet.crushmobile',
        },
        android: {
            package: IS_DEV ? 'com.junknet.crushmobile.dev' : 'com.junknet.crushmobile',
            adaptiveIcon: {
                foregroundImage: './assets/images/adaptive-icon-foreground.png',
                backgroundImage: './assets/images/adaptive-icon-background.png',
                backgroundColor: '#090b10',
            },
            usesCleartextTraffic: true,
            versionCode: ANDROID_VERSION_CODE,
            permissions: ['android.permission.REQUEST_INSTALL_PACKAGES'],
            navigationBar: {
                backgroundColor: '#0a0d14',
                barStyle: 'light-content',
            },
        },
        web: {
            bundler: 'metro',
            output: 'static',
            favicon: './assets/images/adaptive-icon.png',
        },
        plugins: [
            'expo-router',
            [
                'expo-build-properties',
                {
                    android: {
                        usesCleartextTraffic: true,
                    },
                },
            ],
            withDarkAndroidSystemBars,
        ],
        extra: {
            router: {
                origin: false,
            },
            update: {
                githubRepo: process.env.EXPO_PUBLIC_CRUSH_UPDATE_REPO || 'junknet/crush',
                githubToken: process.env.EXPO_PUBLIC_CRUSH_UPDATE_GITHUB_TOKEN || '',
                releaseChannel: process.env.EXPO_PUBLIC_CRUSH_UPDATE_CHANNEL || 'crush-mobile',
                assetPattern:
                    process.env.EXPO_PUBLIC_CRUSH_UPDATE_ASSET_PATTERN ||
                    '^crush-mobile-android-v.*\\.apk$',
            },
        },
    },
}
