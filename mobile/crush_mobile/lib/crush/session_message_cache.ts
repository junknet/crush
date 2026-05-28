import * as FileSystem from 'expo-file-system/legacy'

import { Message } from './api'

type SessionMessageCache = {
    version: 1
    session_id: string
    saved_at: number
    messages: Message[]
}

const SESSION_MESSAGE_CACHE_VERSION = 1
const SESSION_MESSAGE_CACHE_DIRECTORY = 'session-message-cache'
const MAX_CACHED_MESSAGES_PER_SESSION = 2000

function getCacheDirectory(): string | null {
    if (!FileSystem.documentDirectory) return null
    return `${FileSystem.documentDirectory}${SESSION_MESSAGE_CACHE_DIRECTORY}/`
}

function getCachePath(sessionID: string): string | null {
    const directory = getCacheDirectory()
    if (!directory) return null
    const fileName = sessionID.replace(/[^A-Za-z0-9._-]/g, '_')
    return `${directory}${fileName}.json`
}

function sortMessages(messages: Message[]): Message[] {
    return [...messages].sort((left, right) => (left.created_at || 0) - (right.created_at || 0))
}

export async function readSessionMessageCache(sessionID: string): Promise<Message[]> {
    const cachePath = getCachePath(sessionID)
    if (!cachePath) return []

    try {
        const fileInfo = await FileSystem.getInfoAsync(cachePath)
        if (!fileInfo.exists) return []
        const rawCache = await FileSystem.readAsStringAsync(cachePath)
        const cache = JSON.parse(rawCache) as SessionMessageCache
        if (
            cache.version !== SESSION_MESSAGE_CACHE_VERSION ||
            cache.session_id !== sessionID ||
            !Array.isArray(cache.messages)
        ) {
            return []
        }
        return sortMessages(cache.messages)
    } catch (error) {
        console.log('readSessionMessageCache failed:', error)
        return []
    }
}

export async function writeSessionMessageCache(
    sessionID: string,
    messages: Message[]
): Promise<void> {
    const directory = getCacheDirectory()
    const cachePath = getCachePath(sessionID)
    if (!directory || !cachePath) return

    const latestMessages = sortMessages(messages).slice(-MAX_CACHED_MESSAGES_PER_SESSION)
    const cache: SessionMessageCache = {
        version: SESSION_MESSAGE_CACHE_VERSION,
        session_id: sessionID,
        saved_at: Date.now(),
        messages: latestMessages,
    }

    try {
        await FileSystem.makeDirectoryAsync(directory, { intermediates: true })
        await FileSystem.writeAsStringAsync(cachePath, JSON.stringify(cache))
    } catch (error) {
        console.log('writeSessionMessageCache failed:', error)
    }
}

export async function deleteSessionMessageCache(sessionID: string): Promise<void> {
    const cachePath = getCachePath(sessionID)
    if (!cachePath) return
    try {
        await FileSystem.deleteAsync(cachePath, { idempotent: true })
    } catch (error) {
        console.log('deleteSessionMessageCache failed:', error)
    }
}
