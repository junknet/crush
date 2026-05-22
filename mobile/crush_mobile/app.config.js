const IS_DEV = process.env.APP_VARIANT === 'development'

module.exports = {
    expo: {
        name: IS_DEV ? 'Crush Mobile (DEV)' : 'Crush Mobile',
        slug: 'crush-mobile',
        version: '0.1.0',
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
        ],
        extra: {
            router: {
                origin: false,
            },
        },
    },
}
