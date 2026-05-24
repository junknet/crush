import { useLocalSearchParams, useRouter } from 'expo-router'
import { useEffect } from 'react'

function parseServerUrl(value?: string | string[]): string | null {
    if (Array.isArray(value)) {
        return value[0]?.trim() || null
    }
    return value?.trim() || null
}

const Connect = () => {
    const router = useRouter()
    const params = useLocalSearchParams<{ serverUrl?: string | string[] }>()

    useEffect(() => {
        const serverUrl = parseServerUrl(params.serverUrl)
        if (serverUrl) {
            router.replace({ pathname: '/', params: { serverUrl } })
            return
        }
        router.replace('/')
    }, [params.serverUrl, router])

    return null
}

export default Connect
