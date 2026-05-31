import * as FileSystem from 'expo-file-system/legacy'

import { dlog } from './dlog'

// User-controlled chat font scale, persisted across launches. Driven by a
// pinch gesture on the message list. Clamped so text never collapses or blows
// past the bubble width.
export const FONT_SCALE_MIN = 0.8
export const FONT_SCALE_MAX = 1.8
export const FONT_SCALE_DEFAULT = 1

function scalePath(): string | null {
    if (!FileSystem.documentDirectory) return null
    return `${FileSystem.documentDirectory}font-scale.json`
}

export function clampFontScale(value: number): number {
    if (!Number.isFinite(value)) return FONT_SCALE_DEFAULT
    return Math.min(FONT_SCALE_MAX, Math.max(FONT_SCALE_MIN, value))
}

export async function readFontScale(): Promise<number> {
    const path = scalePath()
    if (!path) return FONT_SCALE_DEFAULT
    try {
        const info = await FileSystem.getInfoAsync(path)
        if (!info.exists) return FONT_SCALE_DEFAULT
        const raw = await FileSystem.readAsStringAsync(path)
        const parsed = JSON.parse(raw) as { scale?: number }
        return clampFontScale(parsed.scale ?? FONT_SCALE_DEFAULT)
    } catch (error) {
        dlog('readFontScale failed:', error)
        return FONT_SCALE_DEFAULT
    }
}

export async function writeFontScale(scale: number): Promise<void> {
    const path = scalePath()
    if (!path) return
    try {
        await FileSystem.writeAsStringAsync(path, JSON.stringify({ scale: clampFontScale(scale) }))
    } catch (error) {
        dlog('writeFontScale failed:', error)
    }
}
