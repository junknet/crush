import * as Application from 'expo-application'
import Constants from 'expo-constants'
import * as FileSystem from 'expo-file-system/legacy'
import * as IntentLauncher from 'expo-intent-launcher'
import { Platform } from 'react-native'

type GithubReleaseAsset = {
    name: string
    url: string
    browser_download_url: string
    content_type?: string
    size?: number
}

type GithubRelease = {
    tag_name: string
    name?: string
    draft?: boolean
    prerelease?: boolean
    published_at?: string
    html_url?: string
    assets?: GithubReleaseAsset[]
}

type MobileUpdateConfig = {
    githubRepo: string
    githubToken: string
    releaseChannel: string
    assetPattern: string
}

export type MobileUpdateRelease = {
    version: string
    tagName: string
    releaseName: string
    publishedAt: string
    releaseUrl: string
    asset: {
        name: string
        sizeBytes: number
        downloadApiUrl: string
        browserUrl: string
    }
}

export type MobileUpdateCheckResult = {
    currentVersion: string
    currentBuildVersion: string
    githubRepo: string
    releaseChannel: string
    updateAvailable: boolean
    release: MobileUpdateRelease | null
}

const ANDROID_PACKAGE_ARCHIVE_MIME = 'application/vnd.android.package-archive'
const FLAG_GRANT_READ_URI_PERMISSION = 1
const FLAG_ACTIVITY_NEW_TASK = 0x10000000

function getExpoExtraUpdateConfig(): Partial<MobileUpdateConfig> {
    const extra = Constants.expoConfig?.extra as
        | { update?: Partial<MobileUpdateConfig> }
        | undefined
    return extra?.update || {}
}

function getMobileUpdateConfig(): MobileUpdateConfig {
    const updateConfig = getExpoExtraUpdateConfig()
    return {
        githubRepo:
            updateConfig.githubRepo || process.env.EXPO_PUBLIC_CRUSH_UPDATE_REPO || 'junknet/crush',
        githubToken:
            updateConfig.githubToken || process.env.EXPO_PUBLIC_CRUSH_UPDATE_GITHUB_TOKEN || '',
        releaseChannel:
            updateConfig.releaseChannel ||
            process.env.EXPO_PUBLIC_CRUSH_UPDATE_CHANNEL ||
            'crush-mobile',
        assetPattern:
            updateConfig.assetPattern ||
            process.env.EXPO_PUBLIC_CRUSH_UPDATE_ASSET_PATTERN ||
            '^crush-mobile-android-v.*\\.apk$',
    }
}

function buildGithubHeaders(token: string, accept: string): Record<string, string> {
    const headers: Record<string, string> = {
        Accept: accept,
        'X-GitHub-Api-Version': '2022-11-28',
    }
    if (token.trim()) {
        headers.Authorization = `Bearer ${token.trim()}`
    }
    return headers
}

function getCurrentVersion(): string {
    return Application.nativeApplicationVersion || '0.0.0'
}

function getCurrentBuildVersion(): string {
    return Application.nativeBuildVersion || '0'
}

function extractSemver(rawValue: string): string {
    const match = rawValue.match(/(\d+)\.(\d+)\.(\d+)(?:[-+][0-9A-Za-z.-]+)?/)
    return match?.[0] || '0.0.0'
}

export function compareMobileVersions(left: string, right: string): number {
    const leftParts = extractSemver(left)
        .split(/[.+-]/)[0]
        .split('.')
        .map((part) => Number(part))
    const rightParts = extractSemver(right)
        .split(/[.+-]/)[0]
        .split('.')
        .map((part) => Number(part))

    for (let index = 0; index < 3; index += 1) {
        const leftValue = leftParts[index] || 0
        const rightValue = rightParts[index] || 0
        if (leftValue > rightValue) return 1
        if (leftValue < rightValue) return -1
    }
    return 0
}

function releaseMatchesChannel(release: GithubRelease, releaseChannel: string): boolean {
    const tagName = release.tag_name || ''
    const releaseName = release.name || ''
    return (
        tagName === releaseChannel ||
        tagName.startsWith(`${releaseChannel}-v`) ||
        tagName.startsWith(`${releaseChannel}/v`) ||
        releaseName.includes(`[${releaseChannel}]`)
    )
}

function findAndroidApkAsset(
    release: GithubRelease,
    assetPattern: RegExp
): GithubReleaseAsset | null {
    return (
        release.assets?.find((asset) => {
            if (assetPattern.test(asset.name)) return true
            return (
                asset.name.endsWith('.apk') && asset.content_type === ANDROID_PACKAGE_ARCHIVE_MIME
            )
        }) || null
    )
}

function toMobileUpdateRelease(
    release: GithubRelease,
    asset: GithubReleaseAsset
): MobileUpdateRelease {
    return {
        version: extractSemver(`${release.tag_name} ${release.name || ''} ${asset.name}`),
        tagName: release.tag_name,
        releaseName: release.name || release.tag_name,
        publishedAt: release.published_at || '',
        releaseUrl: release.html_url || '',
        asset: {
            name: asset.name,
            sizeBytes: asset.size || 0,
            downloadApiUrl: asset.url,
            browserUrl: asset.browser_download_url,
        },
    }
}

async function fetchGithubReleases(config: MobileUpdateConfig): Promise<GithubRelease[]> {
    const response = await fetch(
        `https://api.github.com/repos/${config.githubRepo}/releases?per_page=30`,
        {
            headers: buildGithubHeaders(config.githubToken, 'application/vnd.github+json'),
        }
    )
    if (!response.ok) {
        const body = await response.text()
        throw new Error(
            `fetchGithubReleases: GitHub ${response.status} ${response.statusText}: ${body.slice(0, 180)}`
        )
    }
    const payload = await response.json()
    if (!Array.isArray(payload)) {
        throw new Error('fetchGithubReleases: expected GitHub releases array')
    }
    return payload as GithubRelease[]
}

function selectLatestMobileRelease(
    releases: GithubRelease[],
    config: MobileUpdateConfig
): MobileUpdateRelease | null {
    const assetPattern = new RegExp(config.assetPattern)
    const candidates = releases
        .filter((release) => !release.draft)
        .filter((release) => releaseMatchesChannel(release, config.releaseChannel))
        .map((release) => {
            const asset = findAndroidApkAsset(release, assetPattern)
            return asset ? toMobileUpdateRelease(release, asset) : null
        })
        .filter((release): release is MobileUpdateRelease => release !== null)
        .sort((left, right) => compareMobileVersions(right.version, left.version))

    return candidates[0] || null
}

export async function checkForMobileUpdate(): Promise<MobileUpdateCheckResult> {
    const config = getMobileUpdateConfig()
    const releases = await fetchGithubReleases(config)
    const release = selectLatestMobileRelease(releases, config)
    const currentVersion = getCurrentVersion()
    const currentBuildVersion = getCurrentBuildVersion()
    const githubRepo = config.githubRepo
    const releaseChannel = config.releaseChannel
    const updateAvailable = release
        ? compareMobileVersions(release.version, currentVersion) > 0
        : false

    return {
        currentVersion,
        currentBuildVersion,
        githubRepo,
        releaseChannel,
        updateAvailable,
        release,
    }
}

export async function downloadAndOpenAndroidUpdate(
    release: MobileUpdateRelease,
    onProgress?: (progressRatio: number) => void
): Promise<string> {
    if (Platform.OS !== 'android') {
        throw new Error('downloadAndOpenAndroidUpdate: Android only')
    }

    const config = getMobileUpdateConfig()
    const baseDirectory = FileSystem.cacheDirectory || FileSystem.documentDirectory
    if (!baseDirectory) {
        throw new Error('downloadAndOpenAndroidUpdate: no writable app directory')
    }

    const safeAssetName = release.asset.name.replace(/[^A-Za-z0-9._-]/g, '_')
    const fileUri = `${baseDirectory}${safeAssetName}`
    await FileSystem.deleteAsync(fileUri, { idempotent: true })

    const download = FileSystem.createDownloadResumable(
        release.asset.downloadApiUrl,
        fileUri,
        {
            headers: buildGithubHeaders(config.githubToken, 'application/octet-stream'),
        },
        (progress) => {
            const expectedBytes = progress.totalBytesExpectedToWrite
            if (expectedBytes > 0) {
                onProgress?.(progress.totalBytesWritten / expectedBytes)
            }
        }
    )

    const result = await download.downloadAsync()
    if (!result?.uri) {
        throw new Error('downloadAndOpenAndroidUpdate: download did not finish')
    }

    const fileInfo = await FileSystem.getInfoAsync(result.uri)
    if (!fileInfo.exists || !fileInfo.size) {
        throw new Error('downloadAndOpenAndroidUpdate: downloaded APK is empty')
    }

    const contentUri = await FileSystem.getContentUriAsync(result.uri)
    await IntentLauncher.startActivityAsync('android.intent.action.VIEW', {
        data: contentUri,
        type: ANDROID_PACKAGE_ARCHIVE_MIME,
        flags: FLAG_GRANT_READ_URI_PERMISSION | FLAG_ACTIVITY_NEW_TASK,
    })

    return result.uri
}
