import { useFonts } from 'expo-font'
import { Stack } from 'expo-router'
import * as SplashScreen from 'expo-splash-screen'
import { useEffect } from 'react'
import { StatusBar } from 'react-native'
import { GestureHandlerRootView } from 'react-native-gesture-handler'
import { SafeAreaProvider } from 'react-native-safe-area-context'

// Prevent the splash screen from auto-hiding before asset loading is complete.
SplashScreen.preventAutoHideAsync().catch(() => {})

const Layout = () => {
    const [loaded, error] = useFonts({
        'FiraCode-Regular': require('../assets/fonts/FiraCode-Regular.ttf'),
    })

    useEffect(() => {
        if (loaded || error) {
            SplashScreen.hideAsync().catch(() => {})
        }
    }, [loaded, error])

    if (!loaded && !error) {
        return null
    }

    return (
        <GestureHandlerRootView style={{ flex: 1 }}>
            <SafeAreaProvider>
                <StatusBar barStyle="light-content" />
                <Stack
                    screenOptions={{
                        headerShown: false,
                        contentStyle: { backgroundColor: '#090b10' },
                    }}>
                    <Stack.Screen name="index" options={{ animation: 'fade' }} />
                </Stack>
            </SafeAreaProvider>
        </GestureHandlerRootView>
    )
}

export default Layout
