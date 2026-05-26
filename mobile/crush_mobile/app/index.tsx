import { Feather } from '@expo/vector-icons'
import * as Application from 'expo-application'
import { useLocalSearchParams } from 'expo-router'
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
    ActivityIndicator,
    Animated,
    Clipboard,
    Dimensions,
    FlatList,
    KeyboardAvoidingView,
    LayoutAnimation,
    Linking,
    LogBox,
    Modal,
    PanResponder,
    Platform,
    Pressable,
    ScrollView,
    StyleSheet,
    Text,
    TextInput,
    UIManager,
    View,
} from 'react-native'
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context'

import {
    AgentEvent,
    AgentInfo,
    CrushApi,
    CrushEnvelope,
    Message,
    MessagePart,
    PermissionRequest,
    Session,
} from '@lib/crush/api'
import {
    checkForMobileUpdate,
    downloadAndOpenAndroidUpdate,
    MobileUpdateRelease,
} from '@lib/crush/mobile_update'

type ActivityEntry = {
    id: string
    title: string
    profile: string
    prompt: string
    status: 'running' | 'done' | 'failed'
    error?: string
    updatedAt: number
}

type MobileUpdateStatus =
    | 'idle'
    | 'checking'
    | 'latest'
    | 'available'
    | 'downloading'
    | 'opening'
    | 'error'

const DEFAULT_SERVER_URL = process.env.EXPO_PUBLIC_CRUSH_SERVER_URL || 'ws://47.110.255.240:8443'

LogBox.ignoreLogs([
    'Failed to poll workspaces metadata',
    'Failed to poll workspaces',
    'Failed to load active workspaces',
    'Failed to load sessions',
    'Failed to poll events',
    'Failed to get current session',
    'Failed to load agent info',
    'JSON Parse error',
])

if (Platform.OS === 'android') {
    if (UIManager.setLayoutAnimationEnabledExperimental) {
        UIManager.setLayoutAnimationEnabledExperimental(true)
    }
}

const { width: SCREEN_WIDTH } = Dimensions.get('window')
const DRAWER_WIDTH = Math.min(SCREEN_WIDTH * 0.72, 280)

function parseJson(str?: string) {
    if (!str) return null
    try {
        return JSON.parse(str)
    } catch {
        return null
    }
}

function describeError(error: unknown): string {
    return error instanceof Error ? error.message : String(error)
}

function parseServerUrlFromDeepLink(rawUrl: string | null): string | null {
    if (!rawUrl) return null
    try {
        const parsed = new URL(rawUrl)
        return (
            parsed.searchParams.get('serverUrl')?.trim() ||
            parsed.searchParams.get('url')?.trim() ||
            null
        )
    } catch {
        return null
    }
}

function parseServerUrlFromSearchParam(value?: string | string[]): string | null {
    if (Array.isArray(value)) {
        return value[0]?.trim() || null
    }
    return value?.trim() || null
}

function looksLikeInternalSessionTitle(title: string, sessionID: string): boolean {
    const cleaned = title.trim()
    if (!cleaned) return true
    if (cleaned === sessionID) return true
    if (cleaned === 'Untitled Session' || cleaned === '未命名会话') return true
    if (cleaned.startsWith('title-') || cleaned.includes('$$')) return true
    return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(cleaned)
}

function getMessageFinishReason(message?: Message): string {
    return message?.parts?.find((part) => part.type === 'finish')?.data?.reason?.trim() || ''
}

function isMessageFinished(message?: Message): boolean {
    return getMessageFinishReason(message) !== ''
}

function isMessageCanceled(message?: Message): boolean {
    return getMessageFinishReason(message) === 'canceled'
}

function messageHasVisibleContent(message: Message): boolean {
    if (message.role !== 'assistant') return true
    if (message.id === 'typing-indicator') return true
    if (isMessageCanceled(message)) return true
    return (
        message.parts?.some((part) => {
            if (part.type === 'text') return !!part.data?.text?.trim()
            if (part.type === 'reasoning') {
                return !!part.data?.thinking?.trim() || !part.data?.finished_at
            }
            if (part.type === 'tool_call' || part.type === 'tool_result') return true
            if (part.type === 'finish') return false
            return true
        }) || false
    )
}

function getToolCallSummary(name?: string, input?: string): { action: string; details?: string } {
    if (!name) return { action: '未知工具' }
    const parsed = parseJson(input)
    if (!parsed) {
        return { action: name, details: input }
    }

    switch (name) {
        case 'bash':
        case 'run_command':
            return {
                action: '运行命令',
                details: parsed.CommandLine || parsed.command || '',
            }
        case 'view_file':
            return {
                action: '查看文件',
                details: parsed.AbsolutePath || parsed.path || '',
            }
        case 'read_file':
            return {
                action: '读取文件',
                details: parsed.AbsolutePath || parsed.path || '',
            }
        case 'write_to_file':
            return {
                action: '新建文件',
                details: parsed.TargetFile || parsed.path || '',
            }
        case 'replace_file_content':
        case 'multi_replace_file_content':
            return {
                action: '修改文件',
                details: parsed.TargetFile || parsed.path || '',
            }
        case 'grep_search':
            return {
                action: '检索代码',
                details: parsed.Query ? `"${parsed.Query}"` : '',
            }
        case 'list_dir':
            return {
                action: '列出目录',
                details: parsed.DirectoryPath || parsed.path || '',
            }
        case 'ask_permission':
            return {
                action: '请求授权',
                details: `${parsed.Action} -> ${parsed.Target}`,
            }
        default:
            return {
                action: name,
                details:
                    typeof parsed === 'object'
                        ? Object.entries(parsed)
                              .map(([k, v]) => `${k}=${v}`)
                              .join(', ')
                        : String(input),
            }
    }
}

interface Token {
    text: string
    type:
        | 'keyword'
        | 'builtin'
        | 'type'
        | 'class'
        | 'function'
        | 'property'
        | 'constant'
        | 'string'
        | 'regex'
        | 'comment'
        | 'number'
        | 'operator'
        | 'tag'
        | 'attr'
        | 'plain'
}

type ParseState =
    | { type: 'normal' }
    | { type: 'comment_block'; endDelimiter: string }
    | { type: 'string_block'; endDelimiter: string }

function isKeyword(word: string, lang: string): boolean {
    switch (lang) {
        case 'go':
        case 'golang':
            return /^(break|default|func|interface|select|case|defer|go|map|struct|chan|else|goto|package|switch|const|fallthrough|if|range|type|continue|for|import|return|var)$/.test(
                word
            )
        case 'python':
        case 'py':
            return /^(False|None|True|and|as|assert|async|await|break|class|continue|def|del|elif|else|except|finally|for|from|global|if|import|in|is|lambda|nonlocal|not|or|pass|raise|return|try|while|with|yield)$/.test(
                word
            )
        case 'javascript':
        case 'js':
        case 'typescript':
        case 'ts':
        case 'jsx':
        case 'tsx':
            return /^(break|case|catch|class|const|continue|debugger|default|delete|do|else|export|extends|finally|for|function|if|import|in|instanceof|new|return|super|switch|this|throw|try|typeof|var|void|while|with|yield|let|await|async|interface|type|from|as|of)$/.test(
                word
            )
        case 'rust':
        case 'rs':
            return /^(as|async|await|break|const|continue|crate|dyn|else|enum|extern|false|fn|for|if|impl|in|let|loop|match|mod|move|mut|pub|ref|return|self|Self|static|struct|super|trait|true|type|union|unsafe|use|where|while)$/.test(
                word
            )
        case 'nim':
            return /^(addr|and|as|asm|bind|block|break|case|cast|concept|const|continue|converter|defer|discard|distinct|div|do|elif|else|end|enum|except|export|finally|for|from|func|if|import|in|include|interface|is|isnot|iterator|let|macro|method|mixin|mod|nil|not|notin|object|of|or|out|proc|ptr|raise|ref|return|shl|shr|static|template|try|type|using|var|when|while|xor|yield)$/.test(
                word
            )
        case 'css':
        case 'scss':
            return /^(@media|@import|@keyframes|@supports|@font-face|@mixin|@include|@extend|@use|@forward)$/.test(
                word
            )
        case 'json':
            return /^(true|false|null)$/.test(word)
        case 'bash':
        case 'shell':
        case 'sh':
        case 'zsh':
            return /^(if|then|else|elif|fi|case|esac|for|in|do|done|while|until|function|select|time)$/.test(
                word
            )
        default:
            return false
    }
}

function isBuiltin(word: string, lang: string): boolean {
    switch (lang) {
        case 'go':
        case 'golang':
            return /^(append|cap|close|complex|copy|delete|imag|len|make|new|panic|print|println|real|recover)$/.test(
                word
            )
        case 'python':
        case 'py':
            return /^(print|len|range|str|int|float|bool|list|dict|set|tuple|enumerate|zip|map|filter|any|all|sum|max|min|abs|round|open|super|self|cls)$/.test(
                word
            )
        case 'javascript':
        case 'js':
        case 'typescript':
        case 'ts':
        case 'jsx':
        case 'tsx':
            return /^(console|log|error|warn|info|window|document|process|global|require|module|exports|Object|Array|String|Number|Boolean|Function|Symbol|Promise|Set|Map|Error|undefined|null)$/.test(
                word
            )
        case 'rust':
        case 'rs':
            return /^(println|print|format|panic|vec|Ok|Err|Some|None)$/.test(word)
        case 'nim':
            return /^(echo|readLine|stdout|stderr|stdin|new|add|len|repr|high|low|ord|chr|result|assert|quit|defined|declared)$/.test(
                word
            )
        case 'bash':
        case 'shell':
        case 'sh':
        case 'zsh':
            return /^(alias|cd|echo|export|exit|local|return|sudo|git|curl|wget|npm|npx|yarn|pnpm|cargo|go|python|pip)$/.test(
                word
            )
        default:
            return false
    }
}

function isType(word: string, lang: string): boolean {
    switch (lang) {
        case 'go':
        case 'golang':
            return /^(bool|byte|complex64|complex128|error|float32|float64|int|int8|int16|int32|int64|rune|string|uint|uint8|uint16|uint32|uint64|uintptr)$/.test(
                word
            )
        case 'typescript':
        case 'ts':
        case 'jsx':
        case 'tsx':
            return /^(any|string|number|boolean|void|unknown|never|Record|Partial|Omit|Pick)$/.test(
                word
            )
        case 'rust':
        case 'rs':
            return /^(i8|i16|i32|i64|i128|isize|u8|u16|u32|u64|u128|usize|f32|f64|bool|char|str|String|Option|Result|Vec|Box|Rc|Arc)$/.test(
                word
            )
        case 'nim':
            return /^(int|int8|int16|int32|int64|uint|uint8|uint16|uint32|uint64|float|float32|float64|bool|char|string|cstring|pointer|typedesc|auto|any|untyped|typed|void|seq|array|openArray|varargs|set|tuple|ref|ptr)$/.test(
                word
            )
        default:
            return /^[A-Z][a-zA-Z0-9_]*$/.test(word)
    }
}

// Sub-component to render code with advanced syntax highlighting mimicking Tree-sitter
const renderHighlightedCode = (code: string, lang: string) => {
    const normalizedLang = (lang || '').toLowerCase().trim()
    const lines = code.split('\n')
    const tokenizedLines: Token[][] = []

    let state: ParseState = { type: 'normal' }

    for (let i = 0; i < lines.length; i++) {
        const line = lines[i]
        const lineTokens: Token[] = []
        let index = 0
        let lastNonWsToken: Token | null = null

        while (index < line.length) {
            const remaining = line.substring(index)

            // 1. Handle Block Comment state
            if (state.type === 'comment_block') {
                const endIdx = remaining.indexOf(state.endDelimiter)
                if (endIdx !== -1) {
                    const commentText = remaining.substring(0, endIdx + state.endDelimiter.length)
                    const tok: Token = { text: commentText, type: 'comment' }
                    lineTokens.push(tok)
                    index += endIdx + state.endDelimiter.length
                    state = { type: 'normal' }
                    lastNonWsToken = tok
                } else {
                    const tok: Token = { text: remaining, type: 'comment' }
                    lineTokens.push(tok)
                    index += remaining.length
                }
                continue
            }

            // 2. Handle Multi-line String state
            if (state.type === 'string_block') {
                const endIdx = remaining.indexOf(state.endDelimiter)
                if (endIdx !== -1) {
                    const stringText = remaining.substring(0, endIdx + state.endDelimiter.length)
                    const tok: Token = { text: stringText, type: 'string' }
                    lineTokens.push(tok)
                    index += endIdx + state.endDelimiter.length
                    state = { type: 'normal' }
                    lastNonWsToken = tok
                } else {
                    const tok: Token = { text: remaining, type: 'string' }
                    lineTokens.push(tok)
                    index += remaining.length
                }
                continue
            }

            // 3. Match Whitespace
            const wsMatch = remaining.match(/^(\s+)/)
            if (wsMatch) {
                lineTokens.push({ text: wsMatch[0], type: 'plain' })
                index += wsMatch[0].length
                continue
            }

            // 4. Handle comment block initiation
            if (
                [
                    'javascript',
                    'js',
                    'typescript',
                    'ts',
                    'jsx',
                    'tsx',
                    'go',
                    'golang',
                    'css',
                    'scss',
                    'rust',
                    'rs',
                ].includes(normalizedLang)
            ) {
                if (remaining.startsWith('/*')) {
                    state = { type: 'comment_block', endDelimiter: '*/' }
                    const tok: Token = { text: '/*', type: 'comment' }
                    lineTokens.push(tok)
                    index += 2
                    lastNonWsToken = tok
                    continue
                }
            } else if (normalizedLang === 'html' || normalizedLang === 'xml') {
                if (remaining.startsWith('<!--')) {
                    state = { type: 'comment_block', endDelimiter: '-->' }
                    const tok: Token = { text: '<!--', type: 'comment' }
                    lineTokens.push(tok)
                    index += 4
                    lastNonWsToken = tok
                    continue
                }
            }

            // 5. Handle multi-line string block initiation
            if (normalizedLang === 'python' || normalizedLang === 'py') {
                if (remaining.startsWith('"""')) {
                    state = { type: 'string_block', endDelimiter: '"""' }
                    const tok: Token = { text: '"""', type: 'string' }
                    lineTokens.push(tok)
                    index += 3
                    lastNonWsToken = tok
                    continue
                }
                if (remaining.startsWith("'''")) {
                    state = { type: 'string_block', endDelimiter: "'''" }
                    const tok: Token = { text: "'''", type: 'string' }
                    lineTokens.push(tok)
                    index += 3
                    lastNonWsToken = tok
                    continue
                }
            } else if (
                ['javascript', 'js', 'typescript', 'ts', 'jsx', 'tsx', 'go', 'golang'].includes(
                    normalizedLang
                )
            ) {
                if (remaining.startsWith('`')) {
                    state = { type: 'string_block', endDelimiter: '`' }
                    const tok: Token = { text: '`', type: 'string' }
                    lineTokens.push(tok)
                    index += 1
                    lastNonWsToken = tok
                    continue
                }
            }

            let matched = false

            // HTML / XML tag parsing
            if (
                normalizedLang === 'html' ||
                normalizedLang === 'xml' ||
                normalizedLang === 'jsx' ||
                normalizedLang === 'tsx'
            ) {
                const tagMatch = remaining.match(/^<\/?[a-zA-Z][a-zA-Z0-9_-]*/)
                if (tagMatch) {
                    const tok: Token = { text: tagMatch[0], type: 'tag' }
                    lineTokens.push(tok)
                    index += tagMatch[0].length
                    matched = true
                    lastNonWsToken = tok
                    continue
                }
                const tagEndMatch = remaining.match(/^(\/>|>)/)
                if (tagEndMatch) {
                    const tok: Token = { text: tagEndMatch[0], type: 'tag' }
                    lineTokens.push(tok)
                    index += tagEndMatch[0].length
                    matched = true
                    lastNonWsToken = tok
                    continue
                }
                const attrMatch = remaining.match(/^([a-zA-Z0-9_-]+)=/)
                if (attrMatch) {
                    const tok: Token = { text: attrMatch[1], type: 'attr' }
                    lineTokens.push(tok)
                    lineTokens.push({ text: '=', type: 'operator' })
                    index += attrMatch[0].length
                    matched = true
                    lastNonWsToken = tok
                    continue
                }
            }

            // Single-line Comments
            if (
                remaining.startsWith('//') &&
                normalizedLang !== 'python' &&
                normalizedLang !== 'py'
            ) {
                const tok: Token = { text: remaining, type: 'comment' }
                lineTokens.push(tok)
                index += remaining.length
                matched = true
                lastNonWsToken = tok
                continue
            }
            if (
                remaining.startsWith('#') &&
                (normalizedLang === 'python' ||
                    normalizedLang === 'py' ||
                    normalizedLang === 'bash' ||
                    normalizedLang === 'shell' ||
                    normalizedLang === 'sh' ||
                    normalizedLang === 'zsh' ||
                    normalizedLang === 'nim')
            ) {
                const tok: Token = { text: remaining, type: 'comment' }
                lineTokens.push(tok)
                index += remaining.length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Single-line Strings
            const strMatch = remaining.match(/^("(\\.|[^"\\])*"|'(\\.|[^'\\])*')/)
            if (strMatch) {
                const tok: Token = { text: strMatch[0], type: 'string' }
                lineTokens.push(tok)
                index += strMatch[0].length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Regex Literal in JS/TS
            if (
                (normalizedLang === 'javascript' ||
                    normalizedLang === 'js' ||
                    normalizedLang === 'typescript' ||
                    normalizedLang === 'ts' ||
                    normalizedLang === 'jsx' ||
                    normalizedLang === 'tsx') &&
                remaining.startsWith('/')
            ) {
                const isRegexStart =
                    !lastNonWsToken ||
                    lastNonWsToken.type === 'operator' ||
                    (lastNonWsToken.type === 'keyword' &&
                        /^(return|yield|throw|typeof|instanceof|case|else|do)$/.test(
                            lastNonWsToken.text
                        ))

                if (isRegexStart) {
                    const regexMatch = remaining.match(/^\/(\\.|[^\/\n\\])+\/[gimuyvd]*/)
                    if (regexMatch) {
                        const tok: Token = { text: regexMatch[0], type: 'regex' }
                        lineTokens.push(tok)
                        index += regexMatch[0].length
                        matched = true
                        lastNonWsToken = tok
                        continue
                    }
                }
            }

            // Numbers
            const numMatch = remaining.match(
                /^(\b0x[0-9a-fA-F_]+\b|\b0b[01_]+\b|\b\d[\d_]*(\.[\d_]+)?([eE][+-]?\d+)?\b)/
            )
            if (numMatch) {
                const tok: Token = { text: numMatch[0], type: 'number' }
                lineTokens.push(tok)
                index += numMatch[0].length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Decorators / Annotations
            const decMatch = remaining.match(/^(@[a-zA-Z_][a-zA-Z0-9_]*)/)
            if (decMatch) {
                const tok: Token = { text: decMatch[0], type: 'keyword' }
                lineTokens.push(tok)
                index += decMatch[0].length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Identifiers: keywords, builtins, types, classes, functions, properties, variables
            const wordMatch = remaining.match(/^([a-zA-Z_][a-zA-Z0-9_]*)/)
            if (wordMatch) {
                const word = wordMatch[1]
                let type: Token['type'] = 'plain'

                if (isKeyword(word, normalizedLang)) {
                    type = 'keyword'
                } else if (isBuiltin(word, normalizedLang)) {
                    type = 'builtin'
                } else if (isType(word, normalizedLang)) {
                    type = 'type'
                } else {
                    const nextRemaining = remaining.substring(word.length)
                    const isFuncCall = /^\s*\(/.test(nextRemaining)
                    const isPropertyAccess = lastNonWsToken && lastNonWsToken.text === '.'
                    const isPropertyKey =
                        /^\s*:/.test(nextRemaining) &&
                        !/^\s*::/.test(nextRemaining) &&
                        (normalizedLang === 'javascript' ||
                            normalizedLang === 'js' ||
                            normalizedLang === 'typescript' ||
                            normalizedLang === 'ts' ||
                            normalizedLang === 'json' ||
                            normalizedLang === 'go' ||
                            normalizedLang === 'golang')

                    const isClassOrTypeDecl =
                        lastNonWsToken &&
                        (lastNonWsToken.text === 'class' ||
                            lastNonWsToken.text === 'struct' ||
                            lastNonWsToken.text === 'interface' ||
                            (lastNonWsToken.text === 'type' && normalizedLang !== 'python'))

                    const isFuncDecl =
                        lastNonWsToken &&
                        /^(def|func|fn|proc|template|macro)$/.test(lastNonWsToken.text)

                    if (isClassOrTypeDecl) {
                        type = 'class'
                    } else if (isFuncDecl || isFuncCall) {
                        type = 'function'
                    } else if (isPropertyAccess || isPropertyKey) {
                        type = 'property'
                    } else if (/^[A-Z_][A-Z0-9_]+$/.test(word) && word.length >= 2) {
                        type = 'constant'
                    } else if (/^[A-Z][a-zA-Z0-9_]*$/.test(word)) {
                        type = 'class'
                    } else {
                        type = 'plain'
                    }
                }

                const tok: Token = { text: word, type: type }
                lineTokens.push(tok)
                index += word.length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Operators, punctuation
            const symbolMatch = remaining.match(/^([+\-*/%=!&|^~<>?:;.,()[\]{}])/)
            if (symbolMatch) {
                const symbol = symbolMatch[0]
                const tok: Token = { text: symbol, type: 'operator' }
                lineTokens.push(tok)
                index += symbol.length
                matched = true
                lastNonWsToken = tok
                continue
            }

            // Fallback
            if (!matched) {
                const char = remaining[0]
                const tok: Token = { text: char, type: 'plain' }
                lineTokens.push(tok)
                index += 1
                lastNonWsToken = tok
            }
        }

        tokenizedLines.push(lineTokens)
    }

    const getTokenStyle = (type: Token['type']) => {
        switch (type) {
            case 'keyword':
                return { color: '#c084fc', fontWeight: 'bold' as const }
            case 'builtin':
                return { color: '#38bdf8' }
            case 'type':
                return { color: '#2dd4bf' }
            case 'class':
                return { color: '#34d399' }
            case 'function':
                return { color: '#60a5fa' }
            case 'property':
                return { color: '#f472b6' }
            case 'constant':
                return { color: '#fda4af', fontWeight: 'bold' as const }
            case 'string':
                return { color: '#fcd34d' }
            case 'regex':
                return { color: '#f87171' }
            case 'comment':
                return { color: '#64748b', fontStyle: 'italic' as const }
            case 'number':
                return { color: '#f43f5e' }
            case 'operator':
                return { color: '#94a3b8' }
            case 'tag':
                return { color: '#f43f5e', fontWeight: 'bold' as const }
            case 'attr':
                return { color: '#fb923c' }
            default:
                return { color: '#cbd5e1' }
        }
    }

    return tokenizedLines.map((lineTokens, lineIdx) => {
        if (lineTokens.length === 0) {
            return <Text key={lineIdx}>{'\n'}</Text>
        }
        return (
            <Text key={lineIdx} style={styles.markdownCodeBlockText} numberOfLines={0}>
                {lineTokens.map((token, tokenIdx) => (
                    <Text key={tokenIdx} style={getTokenStyle(token.type)}>
                        {token.text}
                    </Text>
                ))}
                {lineIdx < tokenizedLines.length - 1 ? '\n' : ''}
            </Text>
        )
    })
}

// Sub-component for Markdown code blocks with copy & collapse
const MarkdownCodeBlock = ({
    lang,
    code,
    flat,
}: {
    lang: string
    code: string
    flat?: boolean
}) => {
    const [expanded, setExpanded] = useState(false)
    const lines = code.split('\n')
    const needsFolding = lines.length > 12
    const displayCode = needsFolding && !expanded ? lines.slice(0, 12).join('\n') : code

    const handleCopy = () => {
        Clipboard.setString(code)
    }

    const toggle = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }

    return (
        <View
            style={[
                styles.markdownCodeBlockContainer,
                flat && {
                    backgroundColor: 'transparent',
                    borderWidth: 0,
                    borderRadius: 0,
                    padding: 0,
                    marginVertical: 4,
                    shadowColor: 'transparent',
                    shadowOffset: { width: 0, height: 0 },
                    shadowOpacity: 0,
                    shadowRadius: 0,
                    elevation: 0,
                },
            ]}>
            <View
                style={[
                    styles.markdownCodeBlockHeader,
                    flat && {
                        borderBottomWidth: 0,
                        paddingBottom: 0,
                        marginBottom: 4,
                    },
                ]}>
                {lang ? <Text style={styles.markdownCodeBlockLang}>{lang}</Text> : <View />}
                <Pressable
                    onPress={handleCopy}
                    style={({ pressed }) => [
                        styles.copyButton,
                        pressed && styles.pressed,
                        flat && {
                            paddingHorizontal: 4,
                            paddingVertical: 2,
                            backgroundColor: 'transparent',
                        },
                    ]}>
                    <Feather name="copy" size={10} color="#94a3b8" />
                    <Text style={styles.copyButtonText}>复制</Text>
                </Pressable>
            </View>
            <Text style={styles.markdownCodeBlockText} selectable>
                {renderHighlightedCode(displayCode, lang)}
            </Text>
            {needsFolding && (
                <Pressable
                    onPress={toggle}
                    style={({ pressed }) => [styles.expandCodeButton, pressed && styles.pressed]}>
                    <Text style={styles.expandCodeText}>
                        {expanded ? '收起代码' : `展开代码 (余下 ${lines.length - 12} 行)`}
                    </Text>
                </Pressable>
            )}
        </View>
    )
}

// Sub-component for lightweight Markdown rendering
const preprocessHtmlToMarkdown = (rawText: string): string => {
    if (!rawText) return ''
    return rawText
        .replace(/<br\s*\/?>/gi, '\n')
        .replace(/<\/?b>/gi, '**')
        .replace(/<\/?strong>/gi, '**')
        .replace(/<\/?i>/gi, '*')
        .replace(/<\/?em>/gi, '*')
        .replace(/<\/?code>/gi, '`')
}

const renderMarkdownInline = (inlineText: string, flat?: boolean) => {
    if (!inlineText) return ''
    const tokens = inlineText.split(/(\*\*.*?\*\*|\*.*?\*|`.*?`|\[[^\]]+\]\([^)]+\))/g)
    return tokens.map((token, idx) => {
        if (token.startsWith('**') && token.endsWith('**')) {
            return (
                <Text key={idx} style={{ fontWeight: 'bold', color: '#f8fafc' }}>
                    {token.slice(2, -2)}
                </Text>
            )
        }
        if (token.startsWith('*') && token.endsWith('*')) {
            return (
                <Text key={idx} style={{ fontStyle: 'italic', color: '#cbd5e1' }}>
                    {token.slice(1, -1)}
                </Text>
            )
        }
        if (token.startsWith('`') && token.endsWith('`')) {
            return (
                <Text
                    key={idx}
                    style={[
                        styles.markdownInlineCode,
                        flat && {
                            backgroundColor: 'transparent',
                            borderWidth: 0,
                            paddingHorizontal: 0,
                        },
                    ]}>
                    {token.slice(1, -1)}
                </Text>
            )
        }
        if (token.startsWith('[') && token.includes('](')) {
            const match = token.match(/\[([^\]]+)\]\(([^)]+)\)/)
            if (match) {
                const linkText = match[1]
                const url = match[2]
                return (
                    <Text
                        key={idx}
                        style={{ color: '#38bdf8', textDecorationLine: 'underline' }}
                        onPress={() => {
                            Linking.openURL(url).catch((err) =>
                                console.error('Failed to open URL', err)
                            )
                        }}>
                        {linkText}
                    </Text>
                )
            }
        }
        return token
    })
}

const MarkdownDetails = ({
    block,
    style,
    flat,
}: {
    block: string
    style?: any
    flat?: boolean
}) => {
    const [expanded, setExpanded] = useState(false)

    const detailsMatch = block.match(/<details[^>]*>([\s\S]*?)<\/details>/i)
    if (!detailsMatch) return null

    const innerContent = detailsMatch[1]
    const summaryMatch = innerContent.match(/<summary[^>]*>([\s\S]*?)<\/summary>/i)
    let summaryText = 'Details'
    let bodyText = innerContent
    if (summaryMatch) {
        summaryText = summaryMatch[1].trim()
        bodyText = innerContent.replace(/<summary[^>]*>[\s\S]*?<\/summary>/i, '')
    }

    const toggle = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }

    return (
        <View style={styles.detailsBubble}>
            <Pressable
                onPress={toggle}
                style={({ pressed }) => [styles.detailsHeader, pressed && styles.pressed]}>
                <Feather
                    name={expanded ? 'chevron-down' : 'chevron-right'}
                    size={12}
                    color="#a78bfa"
                />
                <Text style={styles.detailsHeaderText}>
                    {renderMarkdownInline(summaryText, flat)}
                </Text>
            </Pressable>
            {expanded && (
                <View style={styles.detailsBody}>
                    <MarkdownText text={bodyText} style={style} flat={flat} />
                </View>
            )}
        </View>
    )
}

// Sub-component for lightweight Markdown rendering
const MarkdownText = React.memo(
    ({ text, style, flat }: { text: string; style?: any; flat?: boolean }) => {
        const preprocessedText = preprocessHtmlToMarkdown(text)
        // Split by code blocks and details blocks
        const blocks = preprocessedText.split(
            /(```[\s\S]*?```|<details[^>]*>[\s\S]*?<\/details>)/gi
        )

        const renderInline = (inlineText: string) => renderMarkdownInline(inlineText, flat)

        return (
            <View style={styles.markdownContainer}>
                {blocks.map((block, blockIdx) => {
                    if (block.startsWith('```') && block.endsWith('```')) {
                        const lines = block.slice(3, -3).trim().split('\n')
                        let lang = ''
                        let codeLines = lines
                        if (lines.length > 0 && /^[a-zA-Z0-9_-]+$/.test(lines[0])) {
                            lang = lines[0]
                            codeLines = lines.slice(1)
                        }
                        const code = codeLines.join('\n')
                        return (
                            <MarkdownCodeBlock key={blockIdx} lang={lang} code={code} flat={flat} />
                        )
                    }

                    const trimmedBlock = block.trim()
                    if (
                        trimmedBlock.toLowerCase().startsWith('<details') &&
                        trimmedBlock.toLowerCase().endsWith('</details>')
                    ) {
                        return (
                            <MarkdownDetails
                                key={blockIdx}
                                block={trimmedBlock}
                                style={style}
                                flat={flat}
                            />
                        )
                    }

                    const lines = block.split('\n')
                    return (
                        <View key={blockIdx} style={styles.markdownTextBlock}>
                            {lines.map((line, lineIdx) => {
                                const headingMatch = line.match(/^(#{1,6})\s+(.*)$/)
                                if (headingMatch) {
                                    const level = headingMatch[1].length
                                    const content = headingMatch[2]
                                    const headingStyle =
                                        level === 1
                                            ? styles.markdownH1
                                            : level === 2
                                              ? styles.markdownH2
                                              : level === 3
                                                ? styles.markdownH3
                                                : styles.markdownH4
                                    return (
                                        <Text key={lineIdx} style={[headingStyle, style]}>
                                            {renderInline(content)}
                                        </Text>
                                    )
                                }

                                const blockquoteMatch = line.match(/^>\s*(.*)$/)
                                if (blockquoteMatch) {
                                    const content = blockquoteMatch[1]
                                    return (
                                        <View
                                            key={lineIdx}
                                            style={[
                                                styles.markdownBlockquote,
                                                flat && {
                                                    backgroundColor: 'transparent',
                                                    borderLeftWidth: 0,
                                                    paddingHorizontal: 0,
                                                    paddingVertical: 0,
                                                    marginVertical: 2,
                                                },
                                            ]}>
                                            <Text style={styles.markdownBlockquoteText}>
                                                {renderInline(content)}
                                            </Text>
                                        </View>
                                    )
                                }

                                if (line === '---' || line === '***' || line === '___') {
                                    return (
                                        <View
                                            key={lineIdx}
                                            style={[
                                                styles.markdownHR,
                                                flat && {
                                                    backgroundColor: 'transparent',
                                                    height: 0,
                                                    marginVertical: 4,
                                                },
                                            ]}
                                        />
                                    )
                                }

                                const listMatch = line.match(/^(\s*)[-*+]\s+(.*)$/)
                                if (listMatch) {
                                    const indent = listMatch[1].length * 4
                                    const content = listMatch[2]
                                    return (
                                        <View
                                            key={lineIdx}
                                            style={[
                                                styles.markdownListItemRow,
                                                { paddingLeft: Math.max(8, indent) },
                                            ]}>
                                            <Text style={styles.markdownListBullet}>•</Text>
                                            <Text style={[styles.markdownListItemText, style]}>
                                                {renderInline(content)}
                                            </Text>
                                        </View>
                                    )
                                }

                                const numListMatch = line.match(/^(\s*)(\d+)\.\s+(.*)$/)
                                if (numListMatch) {
                                    const indent = numListMatch[1].length * 4
                                    const num = numListMatch[2]
                                    const content = numListMatch[3]
                                    return (
                                        <View
                                            key={lineIdx}
                                            style={[
                                                styles.markdownListItemRow,
                                                { paddingLeft: Math.max(8, indent) },
                                            ]}>
                                            <Text style={styles.markdownListNum}>{num}.</Text>
                                            <Text style={[styles.markdownListItemText, style]}>
                                                {renderInline(content)}
                                            </Text>
                                        </View>
                                    )
                                }

                                return (
                                    <Text key={lineIdx} style={[styles.markdownParagraph, style]}>
                                        {renderInline(line)}
                                    </Text>
                                )
                            })}
                        </View>
                    )
                })}
            </View>
        )
    },
    (prev, next) => prev.text === next.text && prev.flat === next.flat
)
MarkdownText.displayName = 'MarkdownText'

// Sub-component for Collapsible Thinking Process
const ThinkingPart = ({ thinking, finishedAt }: { thinking: string; finishedAt?: number }) => {
    const [expanded, setExpanded] = useState(false)
    const isThinking = !finishedAt

    useEffect(() => {
        if (isThinking) {
            LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
            setExpanded(true)
        } else {
            const timer = setTimeout(() => {
                LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
                setExpanded(false)
            }, 1500)
            return () => clearTimeout(timer)
        }
    }, [isThinking])

    if (!thinking && !isThinking) return null

    const toggle = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }

    return (
        <View style={styles.thinkingBubble}>
            <Pressable
                onPress={toggle}
                style={({ pressed }) => [styles.thinkingHeader, pressed && styles.pressed]}>
                <Feather
                    name={expanded ? 'chevron-down' : 'chevron-right'}
                    size={12}
                    color="#c084fc"
                />
                <Text style={styles.thinkingHeaderText}>
                    {isThinking ? 'Agent 正在思考...' : '思考过程 (Reasoning)'}
                </Text>
                {isThinking && (
                    <ActivityIndicator size="small" color="#c084fc" style={{ marginLeft: 6 }} />
                )}
            </Pressable>
            {expanded && thinking ? (
                <View style={styles.thinkingBody}>
                    <Text style={styles.thinkingText}>{thinking}</Text>
                </View>
            ) : null}
        </View>
    )
}

interface AnsiState {
    bold: boolean
    underline: boolean
    color: string | undefined
}

const defaultAnsiState = (): AnsiState => ({
    bold: false,
    underline: false,
    color: undefined,
})

const ANSI_COLORS: Record<string, string> = {
    '30': '#090d16',
    '31': '#ef4444',
    '32': '#10b981',
    '33': '#f59e0b',
    '34': '#3b82f6',
    '35': '#d946ef',
    '36': '#06b6d4',
    '37': '#f1f5f9',
    '90': '#64748b',
    '91': '#f87171',
    '92': '#34d399',
    '93': '#fbbf24',
    '94': '#60a5fa',
    '95': '#f472b6',
    '96': '#22d3ee',
    '97': '#ffffff',
}

const AnsiText = ({ text, style }: { text: string; style?: any }) => {
    const tokens = text.split(/(\u001b\[[0-9;]*m|\x1b\[[0-9;]*m)/g)
    const elements: React.ReactNode[] = []
    let currentState = defaultAnsiState()

    tokens.forEach((token, index) => {
        if (token.startsWith('\u001b[') || token.startsWith('\x1b[')) {
            const code = token.slice(2, -1)
            if (code === '0' || code === '') {
                currentState = defaultAnsiState()
            } else {
                const parts = code.split(';')
                parts.forEach((p) => {
                    if (p === '1') {
                        currentState.bold = true
                    } else if (p === '4') {
                        currentState.underline = true
                    } else if (p === '22') {
                        currentState.bold = false
                    } else if (p === '24') {
                        currentState.underline = false
                    } else if (p === '39') {
                        currentState.color = undefined
                    } else if (ANSI_COLORS[p]) {
                        currentState.color = ANSI_COLORS[p]
                    }
                })
            }
        } else {
            if (token === '') return
            const textStyle: any = {
                fontFamily: 'FiraCode-Regular',
            }
            if (currentState.bold) {
                textStyle.fontWeight = 'bold'
            }
            if (currentState.underline) {
                textStyle.textDecorationLine = 'underline'
            }
            if (currentState.color) {
                textStyle.color = currentState.color
            } else {
                textStyle.color = style?.color || '#cbd5e1'
            }

            elements.push(
                <Text key={index} style={textStyle}>
                    {token}
                </Text>
            )
        }
    })

    return <Text style={style}>{elements}</Text>
}

function cleanTerminalOutput(content?: string): string {
    if (!content) return ''
    let cleaned = content.trim()
    if (cleaned.startsWith('<result>')) {
        cleaned = cleaned.substring(8).trim()
    }
    if (cleaned.endsWith('</result>')) {
        cleaned = cleaned.substring(0, cleaned.length - 9).trim()
    }
    cleaned = cleaned.replace(/<cwd>[\s\S]*?<\/cwd>/gi, '').trim()

    const lines = cleaned.split('\n')
    const processedLines = lines.map((line) => {
        if (line.includes('\r')) {
            const subparts = line.split('\r')
            return subparts[subparts.length - 1]
        }
        return line
    })

    return processedLines.join('\n')
}

type RenderablePart =
    | { type: 'text'; data: { text?: string } }
    | { type: 'reasoning'; data: { thinking?: string; finished_at?: number } }
    | {
          type: 'terminal_session'
          toolCall: { name?: string; input?: string; finished?: boolean }
          toolResult?: { content?: string; is_error?: boolean }
      }

function groupMessageParts(parts: MessagePart[]): RenderablePart[] {
    const grouped: RenderablePart[] = []

    // 1. Gather all tool results mapping tool_call_id -> data
    const toolResultsMap = new Map<string, MessagePart['data']>()
    for (const part of parts) {
        if (part.type === 'tool_result' && part.data && part.data.tool_call_id) {
            toolResultsMap.set(part.data.tool_call_id, part.data)
        }
    }

    // Track which tool results have been associated with a tool call
    const consumedResultIds = new Set<string>()

    let i = 0
    while (i < parts.length) {
        const part = parts[i]
        if (part.type === 'tool_call') {
            const toolCall = part.data
            let toolResult: MessagePart['data'] | undefined = undefined

            if (toolCall.id && toolResultsMap.has(toolCall.id)) {
                toolResult = toolResultsMap.get(toolCall.id)
                consumedResultIds.add(toolCall.id)
            } else {
                // Fallback to sequential check if ID match not found
                if (i + 1 < parts.length && parts[i + 1].type === 'tool_result') {
                    const nextPartData = parts[i + 1].data
                    if (!nextPartData.tool_call_id || nextPartData.tool_call_id === toolCall.id) {
                        toolResult = nextPartData
                        if (nextPartData.tool_call_id) {
                            consumedResultIds.add(nextPartData.tool_call_id)
                        }
                    }
                }
            }

            grouped.push({
                type: 'terminal_session',
                toolCall: {
                    name: toolCall.name,
                    input: toolCall.input,
                    finished: toolCall.finished,
                },
                toolResult: toolResult
                    ? {
                          content: toolResult.content,
                          is_error: toolResult.is_error || (toolResult as any).isError,
                      }
                    : undefined,
            })
            i += 1
        } else if (part.type === 'tool_result') {
            const toolResult = part.data
            if (toolResult.tool_call_id && consumedResultIds.has(toolResult.tool_call_id)) {
                i += 1
                continue
            }
            grouped.push({
                type: 'terminal_session',
                toolCall: {
                    name: '未知工具',
                    input: '',
                    finished: true,
                },
                toolResult: {
                    content: toolResult.content,
                    is_error: toolResult.is_error,
                },
            })
            i += 1
        } else if (part.type === 'text') {
            grouped.push({
                type: 'text',
                data: { text: part.data.text },
            })
            i += 1
        } else if (part.type === 'reasoning') {
            grouped.push({
                type: 'reasoning',
                data: {
                    thinking: part.data.thinking,
                    finished_at: part.data.finished_at,
                },
            })
            i += 1
        } else if (part.type === 'finish') {
            // Explicitly ignore finish control parts in UI bubble list
            i += 1
        } else {
            // Only push as text if there is explicit text data to avoid raw metadata JSON bubbles
            if (part.data && part.data.text) {
                grouped.push({
                    type: 'text',
                    data: { text: part.data.text },
                })
            }
            i += 1
        }
    }
    return grouped
}

// --- Structured Parsers & Types ---

interface FileTreeNode {
    name: string
    isDir: boolean
    level: number
    path: string
}

function parseDirectoryTree(text: string): FileTreeNode[] {
    if (!text) return []
    const lines = text.split('\n')
    const nodes: FileTreeNode[] = []
    let currentPathStack: string[] = []

    for (let line of lines) {
        if (!line.trim()) continue
        const match = line.match(/^(\s*)-\s+(.*)$/)
        if (match) {
            const indent = match[1].length
            const level = Math.floor(indent / 2)
            let name = match[2]
            let isDir = false
            if (name.endsWith('/')) {
                isDir = true
                name = name.slice(0, -1)
            }

            if (level === 0) {
                currentPathStack = [name]
            } else {
                currentPathStack = currentPathStack.slice(0, level)
                currentPathStack.push(name)
            }
            const path = currentPathStack.join('/')

            nodes.push({ name, isDir, level, path })
        }
    }
    return nodes
}

interface SearchMatchLine {
    line: number
    char?: number
    content: string
}

interface SearchFileMatch {
    file: string
    matches: SearchMatchLine[]
}

function parseSearchMatches(text: string): SearchFileMatch[] {
    if (!text) return []
    const lines = text.split('\n')
    const results: SearchFileMatch[] = []
    let currentFileIndex = -1

    for (let line of lines) {
        const trimmed = line.trim()
        if (!trimmed) continue
        if (trimmed.startsWith('Found ') && trimmed.endsWith(' matches')) continue

        if (trimmed.endsWith(':')) {
            const filepath = trimmed.slice(0, -1)
            results.push({ file: filepath, matches: [] })
            currentFileIndex = results.length - 1
        } else if (currentFileIndex >= 0) {
            const matchLine = trimmed.match(/^Line\s+(\d+)(?:,\s+Char\s+(\d+))?:\s*(.*)$/i)
            if (matchLine) {
                const lineNum = parseInt(matchLine[1], 10)
                const charNum = matchLine[2] ? parseInt(matchLine[2], 10) : undefined
                const content = matchLine[3]
                results[currentFileIndex].matches.push({
                    line: lineNum,
                    char: charNum,
                    content: content,
                })
            } else {
                const fallbackMatch = trimmed.match(/^Line\s+(\d+):\s*(.*)$/i)
                if (fallbackMatch) {
                    results[currentFileIndex].matches.push({
                        line: parseInt(fallbackMatch[1], 10),
                        content: fallbackMatch[2],
                    })
                } else {
                    results[currentFileIndex].matches.push({ line: 0, content: trimmed })
                }
            }
        }
    }
    return results
}

interface DiffLine {
    type: 'add' | 'delete' | 'info' | 'normal'
    content: string
}

function parseTextDiff(text: string): boolean {
    if (!text) return false
    const lines = text.split('\n')
    let isDiff = false
    for (const line of lines) {
        if (
            line.startsWith('--- ') ||
            line.startsWith('+++ ') ||
            line.startsWith('@@ ') ||
            line.startsWith('diff --git')
        ) {
            isDiff = true
            break
        }
    }
    if (!isDiff) {
        let plusCount = 0
        let minusCount = 0
        for (const line of lines) {
            if (line.startsWith('+') && !line.startsWith('+++')) plusCount++
            if (line.startsWith('-') && !line.startsWith('---')) minusCount++
        }
        if (plusCount > 1 && minusCount > 1 && (plusCount + minusCount) / lines.length > 0.4) {
            isDiff = true
        }
    }
    return isDiff
}

function parseDiffLines(text: string): DiffLine[] {
    const lines = text.split('\n')
    return lines.map((line) => {
        if (line.startsWith('+') && !line.startsWith('+++')) {
            return { type: 'add', content: line.slice(1) }
        } else if (line.startsWith('-') && !line.startsWith('---')) {
            return { type: 'delete', content: line.slice(1) }
        } else if (line.startsWith('@@')) {
            return { type: 'info', content: line }
        } else {
            return { type: 'normal', content: line }
        }
    })
}

// --- Visual Viewer Sub-components ---

const DirectoryTreeViewer = ({ nodes }: { nodes: FileTreeNode[] }) => {
    const [collapsedPaths, setCollapsedPaths] = useState<Record<string, boolean>>({})

    const toggleCollapse = (path: string) => {
        setCollapsedPaths((prev) => ({ ...prev, [path]: !prev[path] }))
    }

    const visibleNodes = useMemo(() => {
        return nodes.filter((node) => {
            if (node.level === 0) return true
            const parts = node.path.split('/')
            for (let i = 1; i < parts.length; i++) {
                const ancestorPath = parts.slice(0, i).join('/')
                if (collapsedPaths[ancestorPath]) {
                    return false
                }
            }
            return true
        })
    }, [nodes, collapsedPaths])

    return (
        <View style={styles.treeContainer}>
            {visibleNodes.map((node, idx) => {
                const isCollapsed = collapsedPaths[node.path]
                return (
                    <Pressable
                        key={idx}
                        onPress={() => node.isDir && toggleCollapse(node.path)}
                        style={({ pressed }) => [
                            styles.treeRow,
                            { paddingLeft: Math.max(node.level * 14 + 6, 6) },
                            pressed && styles.pressed,
                        ]}>
                        {node.isDir ? (
                            <Feather
                                name={isCollapsed ? 'chevron-right' : 'chevron-down'}
                                size={11}
                                color="#64748b"
                                style={{ marginRight: 4 }}
                            />
                        ) : (
                            <View style={{ width: 15 }} />
                        )}
                        <Feather
                            name={node.isDir ? 'folder' : 'file-text'}
                            size={13}
                            color={node.isDir ? '#38bdf8' : '#94a3b8'}
                            style={{ marginRight: 6 }}
                        />
                        <Text
                            style={[styles.treeNodeName, node.isDir && styles.treeNodeDirectory]}
                            numberOfLines={1}>
                            {node.name}
                        </Text>
                    </Pressable>
                )
            })}
        </View>
    )
}

const SearchMatchesViewer = ({ files }: { files: SearchFileMatch[] }) => {
    const [collapsedFiles, setCollapsedFiles] = useState<Record<string, boolean>>({})

    const toggleCollapse = (file: string) => {
        setCollapsedFiles((prev) => ({ ...prev, [file]: !prev[file] }))
    }

    return (
        <View style={styles.searchMatchesContainer}>
            {files.map((fileMatch, idx) => {
                const isCollapsed = collapsedFiles[fileMatch.file]
                const fileBasename = fileMatch.file.split('/').pop() || fileMatch.file

                return (
                    <View key={idx} style={styles.matchFileCard}>
                        <Pressable
                            onPress={() => toggleCollapse(fileMatch.file)}
                            style={styles.matchFileHeader}>
                            <Feather
                                name={isCollapsed ? 'chevron-right' : 'chevron-down'}
                                size={12}
                                color="#94a3b8"
                                style={{ marginRight: 4 }}
                            />
                            <Feather
                                name="file"
                                size={13}
                                color="#38bdf8"
                                style={{ marginRight: 6 }}
                            />
                            <Text style={styles.matchFileName} numberOfLines={1}>
                                {fileBasename}
                            </Text>
                            <Text style={styles.matchFilePath} numberOfLines={1}>
                                {fileMatch.file.replace(fileBasename, '')}
                            </Text>
                            <View style={styles.matchCountBadge}>
                                <Text style={styles.matchCountText}>
                                    {fileMatch.matches.length}
                                </Text>
                            </View>
                        </Pressable>

                        {!isCollapsed && (
                            <View style={styles.matchLinesList}>
                                {fileMatch.matches.map((m, mIdx) => (
                                    <View key={mIdx} style={styles.matchLineRow}>
                                        <View style={styles.matchLineNumberCol}>
                                            <Text style={styles.matchLineNumber}>{m.line}</Text>
                                        </View>
                                        <ScrollView
                                            horizontal
                                            style={styles.matchLineContentCol}
                                            contentContainerStyle={{ minWidth: '100%' }}>
                                            <Text style={styles.matchLineContentText} selectable>
                                                {m.content}
                                            </Text>
                                        </ScrollView>
                                    </View>
                                ))}
                            </View>
                        )}
                    </View>
                )
            })}
        </View>
    )
}

const DiffViewer = ({ diffLines }: { diffLines: DiffLine[] }) => {
    return (
        <View style={styles.diffContainer}>
            {diffLines.map((line, idx) => {
                let rowBg = '#020617'
                let textColor = '#cbd5e1'
                let sign = ' '
                if (line.type === 'add') {
                    rowBg = '#1665341a'
                    textColor = '#4ade80'
                    sign = '+'
                } else if (line.type === 'delete') {
                    rowBg = '#991b1b1a'
                    textColor = '#f87171'
                    sign = '-'
                } else if (line.type === 'info') {
                    rowBg = '#0c4a6e1a'
                    textColor = '#38bdf8'
                    sign = '@'
                }

                return (
                    <View key={idx} style={[styles.diffRow, { backgroundColor: rowBg }]}>
                        <Text style={[styles.diffSign, { color: textColor }]}>{sign}</Text>
                        <ScrollView
                            horizontal
                            style={styles.diffLineContent}
                            contentContainerStyle={{ minWidth: '100%' }}>
                            <Text style={[styles.diffLineText, { color: textColor }]} selectable>
                                {line.content}
                            </Text>
                        </ScrollView>
                    </View>
                )
            })}
        </View>
    )
}

const FileContentViewer = ({ content, lang }: { content: string; lang: string }) => {
    const lines = content.split('\n')
    const [wrapText, setWrapText] = useState(false)

    return (
        <View style={styles.fileViewerContainer}>
            <View style={styles.fileViewerToolbar}>
                <Text style={styles.fileViewerLangBadge}>{lang || 'text'}</Text>
                <Pressable
                    onPress={() => setWrapText(!wrapText)}
                    style={[styles.wrapToggleBtn, wrapText && styles.wrapToggleBtnActive]}>
                    <Feather name="align-left" size={10} color={wrapText ? '#fff' : '#94a3b8'} />
                    <Text style={[styles.wrapToggleText, wrapText && styles.wrapToggleTextActive]}>
                        自动换行
                    </Text>
                </Pressable>
            </View>
            <ScrollView
                horizontal={!wrapText}
                style={styles.fileViewerScroll}
                contentContainerStyle={!wrapText ? { minWidth: '100%' } : undefined}>
                <View style={styles.fileViewerCodeBlock}>
                    {lines.map((line, idx) => (
                        <View key={idx} style={styles.codeLineRow}>
                            <Text style={styles.codeLineNumber}>{idx + 1}</Text>
                            <Text
                                style={[
                                    styles.codeLineText,
                                    wrapText && { flexWrap: 'wrap', width: SCREEN_WIDTH - 60 },
                                ]}
                                selectable>
                                {renderHighlightedCode(line, lang)}
                            </Text>
                        </View>
                    ))}
                </View>
            </ScrollView>
        </View>
    )
}

const TerminalSessionCard = ({
    name,
    input,
    finished,
    result,
    onMaximize,
}: {
    name?: string
    input?: string
    finished?: boolean
    result?: { content?: string; is_error?: boolean }
    onMaximize?: (name: string, input: string, content: string, isError?: boolean) => void
}) => {
    const [expanded, setExpanded] = useState(false)
    const [activeTab, setActiveTab] = useState<'formatted' | 'raw'>('formatted')

    const summary = useMemo(() => getToolCallSummary(name, input), [name, input])

    const displayableContent = useMemo(() => {
        return cleanTerminalOutput(result?.content)
    }, [result?.content])

    const hasOutput = !!displayableContent
    const lines = displayableContent ? displayableContent.split('\n') : []
    const needsTruncation = lines.length > 12 || displayableContent.length > 800
    const displayContent = expanded ? displayableContent : lines.slice(0, 12).join('\n')

    const fileLanguage = useMemo(() => {
        const parsed = parseJson(input)
        const filePath = parsed?.AbsolutePath || parsed?.TargetFile || parsed?.path || ''
        if (filePath) {
            const ext = filePath.split('.').pop()?.toLowerCase()
            return ext || 'text'
        }
        return 'text'
    }, [input])

    const isError = result?.is_error || (result as any)?.isError

    const viewType = useMemo(() => {
        if (!hasOutput) return 'none'
        if (isError) return 'raw'

        if (name === 'grep_search' || name === 'grep') {
            const parsed = parseSearchMatches(displayableContent)
            if (parsed.length > 0) return 'grep'
        }

        if (name === 'list_dir' || name === 'ls') {
            const parsed = parseDirectoryTree(displayableContent)
            if (parsed.length > 0) return 'ls'
        }

        if (name === 'view_file' || name === 'read_file') {
            return 'file_view'
        }

        if (parseTextDiff(displayableContent)) {
            return 'diff'
        }

        return 'raw'
    }, [name, displayableContent, hasOutput, isError])

    const toggleExpand = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }

    return (
        <View style={[styles.terminalCardContainer, isError && styles.terminalCardError]}>
            <View style={styles.terminalHeader}>
                <View style={styles.terminalHeaderLeft}>
                    <View style={[styles.macDot, { backgroundColor: '#ef4444' }]} />
                    <View style={[styles.macDot, { backgroundColor: '#f59e0b' }]} />
                    <View style={[styles.macDot, { backgroundColor: '#10b981' }]} />
                </View>
                <Text style={styles.terminalHeaderTitle}>{name || 'terminal'}</Text>
                <View style={styles.terminalHeaderRight}>
                    {!finished ? (
                        <ActivityIndicator size="small" color="#38bdf8" />
                    ) : isError ? (
                        <Feather name="alert-triangle" size={12} color="#f87171" />
                    ) : (
                        <Feather name="check" size={12} color="#34d399" />
                    )}
                    {hasOutput && onMaximize && (
                        <Pressable
                            onPress={() =>
                                onMaximize(
                                    name || 'terminal',
                                    input || '',
                                    displayableContent,
                                    isError
                                )
                            }
                            style={styles.terminalHeaderButton}>
                            <Feather name="maximize-2" size={11} color="#94a3b8" />
                        </Pressable>
                    )}
                </View>
            </View>

            <View style={styles.terminalCommandRow}>
                <Text style={styles.terminalPrompt}>crush ❯</Text>
                {(name === 'bash' || name === 'run_command') && summary.details ? (
                    <Text style={styles.terminalCommandText} selectable numberOfLines={0}>
                        {renderHighlightedCode(summary.details, 'bash')}
                    </Text>
                ) : (
                    <Text style={styles.terminalCommandText} selectable>
                        {summary.details || name}
                    </Text>
                )}
            </View>

            {viewType !== 'raw' && viewType !== 'none' && (
                <View style={styles.terminalTabsRow}>
                    <Pressable
                        onPress={() => setActiveTab('formatted')}
                        style={[
                            styles.terminalTabButton,
                            activeTab === 'formatted' && styles.terminalTabButtonActive,
                        ]}>
                        <Feather
                            name="eye"
                            size={11}
                            color={activeTab === 'formatted' ? '#38bdf8' : '#64748b'}
                        />
                        <Text
                            style={[
                                styles.terminalTabText,
                                activeTab === 'formatted' && styles.terminalTabTextActive,
                            ]}>
                            可视化
                        </Text>
                    </Pressable>
                    <Pressable
                        onPress={() => setActiveTab('raw')}
                        style={[
                            styles.terminalTabButton,
                            activeTab === 'raw' && styles.terminalTabButtonActive,
                        ]}>
                        <Feather
                            name="terminal"
                            size={11}
                            color={activeTab === 'raw' ? '#38bdf8' : '#64748b'}
                        />
                        <Text
                            style={[
                                styles.terminalTabText,
                                activeTab === 'raw' && styles.terminalTabTextActive,
                            ]}>
                            终端日志
                        </Text>
                    </Pressable>
                </View>
            )}

            {hasOutput && (
                <View style={styles.terminalOutputContainer}>
                    {activeTab === 'formatted' && viewType !== 'raw' ? (
                        <View
                            style={[
                                { paddingVertical: 4 },
                                needsTruncation &&
                                    !expanded && { maxHeight: 220, overflow: 'hidden' },
                            ]}>
                            {viewType === 'ls' && (
                                <DirectoryTreeViewer
                                    nodes={parseDirectoryTree(displayableContent)}
                                />
                            )}
                            {viewType === 'grep' && (
                                <SearchMatchesViewer
                                    files={parseSearchMatches(displayableContent)}
                                />
                            )}
                            {viewType === 'file_view' && (
                                <FileContentViewer
                                    content={displayableContent}
                                    lang={fileLanguage}
                                />
                            )}
                            {viewType === 'diff' && (
                                <DiffViewer diffLines={parseDiffLines(displayableContent)} />
                            )}
                        </View>
                    ) : (
                        <ScrollView
                            horizontal
                            style={styles.terminalOutputScroll}
                            contentContainerStyle={{ minWidth: '100%' }}>
                            <View style={{ flexDirection: 'column' }}>
                                <AnsiText
                                    text={displayContent}
                                    style={[
                                        styles.terminalOutputText,
                                        isError && { color: '#f87171' },
                                    ]}
                                />
                            </View>
                        </ScrollView>
                    )}

                    {needsTruncation && (
                        <View style={styles.terminalFoldOverlay}>
                            <Pressable onPress={toggleExpand} style={styles.terminalExpandButton}>
                                <Text style={styles.terminalExpandText}>
                                    {expanded
                                        ? '收起输出'
                                        : activeTab === 'formatted' && viewType !== 'raw'
                                          ? '展开全部输出...'
                                          : `展开余下 ${lines.length - 12} 行...`}
                                </Text>
                            </Pressable>
                        </View>
                    )}
                </View>
            )}
        </View>
    )
}

const FullTerminalModal = ({
    visible,
    title,
    input,
    content,
    isError,
    onClose,
}: {
    visible: boolean
    title: string
    input: string
    content: string
    isError?: boolean
    onClose: () => void
}) => {
    const [fontSize, setFontSize] = useState(11)
    const [activeTab, setActiveTab] = useState<'formatted' | 'raw'>('formatted')

    const handleCopy = () => {
        Clipboard.setString(content)
    }

    const increaseFont = () => setFontSize((prev) => Math.min(prev + 1, 18))
    const decreaseFont = () => setFontSize((prev) => Math.max(prev - 1, 8))

    const fileLanguage = useMemo(() => {
        const parsed = parseJson(input)
        const filePath = parsed?.AbsolutePath || parsed?.TargetFile || parsed?.path || ''
        if (filePath) {
            const ext = filePath.split('.').pop()?.toLowerCase()
            return ext || 'text'
        }
        return 'text'
    }, [input])

    const viewType = useMemo(() => {
        if (!content) return 'none'
        if (isError) return 'raw'

        if (title === 'grep_search' || title === 'grep') {
            const parsed = parseSearchMatches(content)
            if (parsed.length > 0) return 'grep'
        }

        if (title === 'list_dir' || title === 'ls') {
            const parsed = parseDirectoryTree(content)
            if (parsed.length > 0) return 'ls'
        }

        if (title === 'view_file' || title === 'read_file') {
            return 'file_view'
        }

        if (parseTextDiff(content)) {
            return 'diff'
        }

        return 'raw'
    }, [title, content, isError])

    return (
        <Modal visible={visible} animationType="slide" transparent={true} onRequestClose={onClose}>
            <SafeAreaView style={styles.modalBg}>
                <View style={styles.modalContent}>
                    <View style={styles.modalHeader}>
                        <View style={styles.modalHeaderLeft}>
                            <Text style={styles.modalTitle}>{title} - 完整输出</Text>
                        </View>
                        <View style={styles.modalHeaderRight}>
                            {activeTab === 'raw' && (
                                <>
                                    <Pressable onPress={decreaseFont} style={styles.modalHeaderBtn}>
                                        <Feather name="minus" size={14} color="#94a3b8" />
                                    </Pressable>
                                    <Text style={styles.modalFontText}>{fontSize}px</Text>
                                    <Pressable onPress={increaseFont} style={styles.modalHeaderBtn}>
                                        <Feather name="plus" size={14} color="#94a3b8" />
                                    </Pressable>
                                </>
                            )}
                            <Pressable onPress={handleCopy} style={styles.modalHeaderBtn}>
                                <Feather name="copy" size={14} color="#94a3b8" />
                            </Pressable>
                            <Pressable
                                onPress={onClose}
                                style={[styles.modalHeaderBtn, styles.modalCloseBtn]}>
                                <Feather name="x" size={16} color="#f87171" />
                            </Pressable>
                        </View>
                    </View>

                    {viewType !== 'raw' && viewType !== 'none' && (
                        <View style={styles.terminalTabsRow}>
                            <Pressable
                                onPress={() => setActiveTab('formatted')}
                                style={[
                                    styles.terminalTabButton,
                                    activeTab === 'formatted' && styles.terminalTabButtonActive,
                                ]}>
                                <Feather
                                    name="eye"
                                    size={11}
                                    color={activeTab === 'formatted' ? '#38bdf8' : '#64748b'}
                                />
                                <Text
                                    style={[
                                        styles.terminalTabText,
                                        activeTab === 'formatted' && styles.terminalTabTextActive,
                                    ]}>
                                    可视化
                                </Text>
                            </Pressable>
                            <Pressable
                                onPress={() => setActiveTab('raw')}
                                style={[
                                    styles.terminalTabButton,
                                    activeTab === 'raw' && styles.terminalTabButtonActive,
                                ]}>
                                <Feather
                                    name="terminal"
                                    size={11}
                                    color={activeTab === 'raw' ? '#38bdf8' : '#64748b'}
                                />
                                <Text
                                    style={[
                                        styles.terminalTabText,
                                        activeTab === 'raw' && styles.terminalTabTextActive,
                                    ]}>
                                    终端日志
                                </Text>
                            </Pressable>
                        </View>
                    )}

                    <ScrollView
                        style={styles.modalLogScroll}
                        contentContainerStyle={styles.modalLogScrollContent}>
                        {activeTab === 'formatted' && viewType !== 'raw' && viewType !== 'none' ? (
                            <View style={{ padding: 12 }}>
                                {viewType === 'ls' && (
                                    <DirectoryTreeViewer nodes={parseDirectoryTree(content)} />
                                )}
                                {viewType === 'grep' && (
                                    <SearchMatchesViewer files={parseSearchMatches(content)} />
                                )}
                                {viewType === 'file_view' && (
                                    <FileContentViewer content={content} lang={fileLanguage} />
                                )}
                                {viewType === 'diff' && (
                                    <DiffViewer diffLines={parseDiffLines(content)} />
                                )}
                            </View>
                        ) : (
                            <ScrollView horizontal contentContainerStyle={{ minWidth: '100%' }}>
                                <AnsiText
                                    text={content}
                                    style={[
                                        styles.modalLogText,
                                        { fontSize },
                                        isError && { color: '#f87171' },
                                    ]}
                                />
                            </ScrollView>
                        )}
                    </ScrollView>
                </View>
            </SafeAreaView>
        </Modal>
    )
}

const BlinkingCursor = ({ color, size = 10 }: { color: string; size?: number }) => {
    const fadeAnim = useRef(new Animated.Value(1)).current

    useEffect(() => {
        const animation = Animated.loop(
            Animated.sequence([
                Animated.timing(fadeAnim, {
                    toValue: 0.2,
                    duration: 400,
                    useNativeDriver: true,
                }),
                Animated.timing(fadeAnim, {
                    toValue: 1,
                    duration: 400,
                    useNativeDriver: true,
                }),
            ])
        )
        animation.start()
        return () => animation.stop()
    }, [fadeAnim])

    return (
        <Animated.Text
            style={{
                color,
                fontSize: size,
                fontWeight: 'bold',
                fontFamily: 'FiraCode-Regular',
                opacity: fadeAnim,
            }}>
            _
        </Animated.Text>
    )
}

const SPIN_FRAMES = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']

const BrailleSpinner = ({ color, size = 12 }: { color: string; size?: number }) => {
    const [frameIndex, setFrameIndex] = useState(0)

    useEffect(() => {
        const interval = setInterval(() => {
            setFrameIndex((prev) => (prev + 1) % SPIN_FRAMES.length)
        }, 80)
        return () => clearInterval(interval)
    }, [])

    return (
        <Text
            style={{
                color,
                fontSize: size,
                fontWeight: 'bold',
                fontFamily: 'FiraCode-Regular',
            }}>
            {SPIN_FRAMES[frameIndex]}
        </Text>
    )
}

const InterruptDivider = () => (
    <View style={styles.interruptDivider}>
        <View style={styles.interruptDividerLine} />
        <Text style={styles.interruptDividerText}>interrupted</Text>
        <View style={styles.interruptDividerLine} />
    </View>
)

// Sub-component for Message Render
const MessageItem = React.memo(
    ({
        message,
        isUser,
        showHeader,
        isBusy,
        onMaximize,
    }: {
        message: Message
        isUser: boolean
        showHeader: boolean
        isBusy?: boolean
        onMaximize?: (name: string, input: string, content: string, isError?: boolean) => void
    }) => {
        const renderableParts = useMemo(() => {
            return message.parts ? groupMessageParts(message.parts) : []
        }, [message.parts])
        const isCanceled = isMessageCanceled(message)
        const finishMessage =
            message.parts?.find((part) => part.type === 'finish')?.data?.message || 'Canceled'

        const hasComplexParts = useMemo(() => {
            return (
                !isUser &&
                renderableParts.some((p) => p.type === 'terminal_session' || p.type === 'reasoning')
            )
        }, [isUser, renderableParts])

        const isAgentReasoning = useMemo(() => {
            if (!isBusy || isMessageFinished(message)) return false
            if (renderableParts.length === 0) return true
            return renderableParts.some((p) => p.type === 'reasoning' && !p.data?.finished_at)
        }, [message, renderableParts, isBusy])

        if (isUser) {
            return (
                <View style={[styles.messageRowContainer, styles.userRowContainer]}>
                    <View style={styles.userBubble}>
                        {renderableParts.map((part, index) => {
                            if (part.type === 'text') {
                                return (
                                    <MarkdownText
                                        key={index}
                                        text={part.data.text || ''}
                                        style={styles.messageText}
                                    />
                                )
                            }
                            return null
                        })}
                    </View>
                </View>
            )
        }

        if (!hasComplexParts) {
            const hasVisibleContent = renderableParts.length > 0 || isCanceled || isBusy
            if (!hasVisibleContent) {
                return null
            }
            return (
                <View style={[styles.messageRowContainer, styles.assistantRowContainer]}>
                    {showHeader && (
                        <View style={styles.assistantMessageHeader}>
                            {isBusy ? (
                                <View
                                    style={{
                                        flexDirection: 'row',
                                        alignItems: 'center',
                                        columnGap: 1,
                                    }}>
                                    <Text
                                        style={{
                                            color: '#c084fc',
                                            fontSize: 10,
                                            fontWeight: 'bold',
                                            fontFamily: 'FiraCode-Regular',
                                        }}>
                                        ❯
                                    </Text>
                                    <BlinkingCursor color="#c084fc" size={10} />
                                </View>
                            ) : (
                                <Feather name="terminal" size={10} color="#c084fc" />
                            )}
                            <Text style={[styles.messageRoleText, styles.assistantRoleText]}>
                                Agent
                            </Text>
                        </View>
                    )}
                    {renderableParts.length > 0 ? (
                        <View style={styles.assistantBubble}>
                            {renderableParts.map((part, index) => {
                                if (part.type === 'text') {
                                    return (
                                        <MarkdownText
                                            key={index}
                                            text={part.data.text || ''}
                                            style={styles.messageText}
                                        />
                                    )
                                }
                                return null
                            })}
                            {isCanceled && (
                                <View style={styles.canceledContainer}>
                                    <Text style={styles.canceledText}>{finishMessage}</Text>
                                </View>
                            )}
                            {isBusy && !isCanceled && (
                                <View
                                    style={[
                                        styles.typingIndicatorContainer,
                                        {
                                            marginTop: 6,
                                            flexDirection: 'row',
                                            alignItems: 'center',
                                            columnGap: 6,
                                        },
                                    ]}>
                                    {isAgentReasoning ? (
                                        <View
                                            style={{
                                                flexDirection: 'row',
                                                alignItems: 'center',
                                                columnGap: 4,
                                            }}>
                                            <Text style={[styles.typingText, { fontSize: 12 }]}>
                                                正在思考...{' '}
                                            </Text>
                                            <BrailleSpinner color="#c084fc" size={12} />
                                        </View>
                                    ) : (
                                        <Text style={[styles.typingText, { fontSize: 12 }]}>
                                            运行中... ⚙️
                                        </Text>
                                    )}
                                </View>
                            )}
                        </View>
                    ) : isCanceled ? (
                        <View style={styles.assistantBubble}>
                            <View style={styles.canceledContainer}>
                                <Text style={styles.canceledText}>{finishMessage}</Text>
                            </View>
                        </View>
                    ) : (
                        isBusy && (
                            <View style={styles.assistantBubble}>
                                <View style={styles.typingIndicatorContainer}>
                                    {isAgentReasoning ? (
                                        <View
                                            style={{
                                                flexDirection: 'row',
                                                alignItems: 'center',
                                                columnGap: 4,
                                            }}>
                                            <Text style={styles.typingText}>正在思考... </Text>
                                            <BrailleSpinner color="#c084fc" size={12} />
                                        </View>
                                    ) : (
                                        <Text style={styles.typingText}>运行中... ⚙️</Text>
                                    )}
                                </View>
                            </View>
                        )
                    )}
                    {isCanceled && <InterruptDivider />}
                </View>
            )
        }

        return (
            <View style={[styles.messageRowContainer, styles.assistantRowContainer]}>
                {showHeader && (
                    <View style={styles.assistantMessageHeader}>
                        {isBusy ? (
                            <View
                                style={{
                                    flexDirection: 'row',
                                    alignItems: 'center',
                                    columnGap: 1,
                                }}>
                                <Text
                                    style={{
                                        color: '#c084fc',
                                        fontSize: 10,
                                        fontWeight: 'bold',
                                        fontFamily: 'FiraCode-Regular',
                                    }}>
                                    ❯
                                </Text>
                                <BlinkingCursor color="#c084fc" size={10} />
                            </View>
                        ) : (
                            <Feather name="terminal" size={10} color="#c084fc" />
                        )}
                        <Text style={[styles.messageRoleText, styles.assistantRoleText]}>
                            Agent
                        </Text>
                    </View>
                )}
                {renderableParts.map((part, index) => {
                    if (part.type === 'reasoning') {
                        return (
                            <ThinkingPart
                                key={index}
                                thinking={part.data.thinking || ''}
                                finishedAt={part.data.finished_at}
                            />
                        )
                    }
                    if (part.type === 'terminal_session') {
                        return (
                            <View key={index} style={styles.terminalSessionCardWrapper}>
                                <TerminalSessionCard
                                    name={part.toolCall.name}
                                    input={part.toolCall.input}
                                    finished={part.toolCall.finished}
                                    result={part.toolResult}
                                    onMaximize={onMaximize}
                                />
                            </View>
                        )
                    }
                    if (part.type === 'text') {
                        const prevPart = index > 0 ? renderableParts[index - 1] : null
                        const isAfterTerminal = prevPart?.type === 'terminal_session'
                        return (
                            <View
                                key={index}
                                style={[
                                    styles.compoundTextContainer,
                                    isAfterTerminal && { marginTop: 8 },
                                ]}>
                                <MarkdownText
                                    text={part.data.text || ''}
                                    style={styles.messageText}
                                    flat={true}
                                />
                            </View>
                        )
                    }
                    return null
                })}
                {isCanceled && (
                    <View style={styles.canceledContainer}>
                        <Text style={styles.canceledText}>{finishMessage}</Text>
                    </View>
                )}
                {isBusy && !isCanceled && (
                    <View style={styles.compoundTypingContainer}>
                        {isAgentReasoning ? (
                            <View
                                style={{
                                    flexDirection: 'row',
                                    alignItems: 'center',
                                    columnGap: 4,
                                }}>
                                <Text style={styles.compoundTypingText}>正在思考... </Text>
                                <BrailleSpinner color="#c084fc" size={12} />
                            </View>
                        ) : (
                            <Text style={styles.compoundTypingText}>运行中... ⚙️</Text>
                        )}
                    </View>
                )}
                {isCanceled && <InterruptDivider />}
            </View>
        )
    },
    (prev, next) => {
        // Stream tokens mutate message.parts in place, but our reducer in
        // handleEvent always replaces the message object via map.set() — so a
        // changed message has a new object ref. Compare by ref + cheap props.
        // This makes the historical messages (which never change) skip render
        // entirely while the trailing streaming one still updates per flush.
        if (prev.message !== next.message) return false
        if (prev.isUser !== next.isUser) return false
        if (prev.showHeader !== next.showHeader) return false
        if (prev.isBusy !== next.isBusy) return false
        if (prev.onMaximize !== next.onMaximize) return false
        return true
    }
)
MessageItem.displayName = 'MessageItem'

const CrushMobile = () => {
    const insets = useSafeAreaInsets()
    const sendingRef = useRef(false)
    const searchParams = useLocalSearchParams<{ serverUrl?: string | string[] }>()
    const searchServerUrl = useMemo(
        () => parseServerUrlFromSearchParam(searchParams.serverUrl),
        [searchParams.serverUrl]
    )

    const [serverUrl, setServerUrl] = useState(DEFAULT_SERVER_URL)
    const [connectedUrl, setConnectedUrl] = useState(DEFAULT_SERVER_URL)

    const [sessions, setSessions] = useState<Session[]>([])
    const [sessionID, setSessionID] = useState('')
    const sessionIDRef = useRef(sessionID)
    sessionIDRef.current = sessionID
    // O(1) dedupe + insertion ordering. Source of truth; setMessages array
    // is only a render projection refreshed on a 50ms tick so a 10k-event
    // history replay no longer triggers 10k React renders.
    const messagesMapRef = useRef<Map<string, Message>>(new Map())
    const messagesFlushScheduledRef = useRef(false)
    const localCancelMessageIdsRef = useRef<Set<string>>(new Set())
    const [deletingSessionId, setDeletingSessionId] = useState<string | null>(null)
    const [sessionAccessTimes, setSessionAccessTimes] = useState<Record<string, number>>({})

    useEffect(() => {
        if (sessionID) {
            setSessionAccessTimes((prev) => {
                if (prev[sessionID]) return prev
                return {
                    ...prev,
                    [sessionID]: Date.now(),
                }
            })
        }
    }, [sessionID])

    const [messages, setMessages] = useState<Message[]>([])
    const [agentInfo, setAgentInfo] = useState<AgentInfo>({ is_busy: false, is_ready: true })
    const activeRunVisible = useMemo(() => {
        if (!agentInfo.is_busy) return false
        if (messages.length === 0) return false
        const lastMessage = messages[messages.length - 1]
        if (lastMessage.role === 'user') return true
        return messages.some(
            (message) => message.role === 'assistant' && !isMessageFinished(message)
        )
    }, [messages, agentInfo.is_busy])
    const isAgentReasoning = useMemo(() => {
        if (!activeRunVisible || messages.length === 0) return false
        const lastMsg = messages[messages.length - 1]
        if (lastMsg.role !== 'assistant') return false
        if (isMessageFinished(lastMsg)) return false
        return lastMsg.parts?.some((p) => p.type === 'reasoning' && !p.data?.finished_at) || false
    }, [messages, activeRunVisible])
    const [pendingPermissions, setPendingPermissions] = useState<PermissionRequest[]>([])
    const [activities, setActivities] = useState<ActivityEntry[]>([])
    const [input, setInput] = useState('')
    // zsh-style input history: every successfully-sent prompt is pushed onto
    // inputHistory; ↑/↓ on a hardware/Bluetooth keyboard (and scrcpy-mirrored
    // physical keys) walks the stack into the TextInput. -1 means "below the
    // newest entry" so a fresh ↑ from an empty input lands on the latest.
    const [inputHistory, setInputHistory] = useState<string[]>([])
    const [historyIndex, setHistoryIndex] = useState(-1)
    // Draft buffer: stash whatever the user was typing before walking back
    // into history, so ↓ past the newest entry restores it instead of
    // dropping it.
    const inputDraftRef = useRef('')
    const [status, setStatus] = useState('未连接')
    const [errorText, setErrorText] = useState('')
    const [isLoading, setIsLoading] = useState(false)
    const [showConnectionSettings, setShowConnectionSettings] = useState(false)
    // Model selector state: target role + the user-typed provider/model
    // pair. Persisted to TUI state.yaml via api.setModel which routes the
    // command through NATS to the active relay.
    const [modelRole, setModelRole] = useState<'brain' | 'worker' | 'explore'>('brain')
    const [modelProvider, setModelProvider] = useState('')
    const [modelId, setModelId] = useState('')
    const [modelSaveStatus, setModelSaveStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>(
        'idle'
    )
    const [quickSelectRole, setQuickSelectRole] = useState<'brain' | 'worker' | null>(null)
    const [switchingModelRole, setSwitchingModelRole] = useState<'brain' | 'worker' | null>(null)
    const [mobileUpdateStatus, setMobileUpdateStatus] = useState<MobileUpdateStatus>('idle')
    const [mobileUpdateRelease, setMobileUpdateRelease] = useState<MobileUpdateRelease | null>(null)
    const [mobileUpdateError, setMobileUpdateError] = useState('')
    const [mobileUpdateProgress, setMobileUpdateProgress] = useState(0)
    // Collapsed cwd groups in the drawer. A path here means its sessions
    // are hidden. Default empty = all groups expanded. Persisted only in
    // memory; on app relaunch everything is shown.
    const [collapsedCwdGroups, setCollapsedCwdGroups] = useState<Set<string>>(new Set())

    const [terminalModalVisible, setTerminalModalVisible] = useState(false)
    const [terminalModalTitle, setTerminalModalTitle] = useState('')
    const [terminalModalInput, setTerminalModalInput] = useState('')
    const [terminalModalContent, setTerminalModalContent] = useState('')
    const [terminalModalIsError, setTerminalModalIsError] = useState(false)

    const handleMaximizeTerminal = useCallback(
        (name: string, input: string, content: string, isError?: boolean) => {
            setTerminalModalTitle(name)
            setTerminalModalInput(input)
            setTerminalModalContent(content)
            setTerminalModalIsError(!!isError)
            setTerminalModalVisible(true)
        },
        []
    )

    // 手势控制 Drawer 的相关状态与动画
    const drawerTranslateX = useRef(new Animated.Value(-DRAWER_WIDTH)).current
    const [drawerOpen, setDrawerOpen] = useState(false)
    const drawerScrollViewRef = useRef<ScrollView>(null)

    // 呼吸灯动画
    const breatheAnim = useRef(new Animated.Value(0.4)).current
    useEffect(() => {
        Animated.loop(
            Animated.sequence([
                Animated.timing(breatheAnim, {
                    toValue: 1.0,
                    duration: 1200,
                    useNativeDriver: true,
                }),
                Animated.timing(breatheAnim, {
                    toValue: 0.4,
                    duration: 1200,
                    useNativeDriver: true,
                }),
            ])
        ).start()
    }, [breatheAnim])

    // 闪烁灯动画
    const blinkAnim = useRef(new Animated.Value(0.2)).current
    useEffect(() => {
        Animated.loop(
            Animated.sequence([
                Animated.timing(blinkAnim, {
                    toValue: 1.0,
                    duration: 300,
                    useNativeDriver: true,
                }),
                Animated.timing(blinkAnim, {
                    toValue: 0.2,
                    duration: 300,
                    useNativeDriver: true,
                }),
            ])
        ).start()
    }, [blinkAnim])

    const openDrawer = useCallback(() => {
        setDrawerOpen(true)
        Animated.spring(drawerTranslateX, {
            toValue: 0,
            useNativeDriver: true,
            bounciness: 2,
            speed: 12,
        }).start()
    }, [drawerTranslateX])

    const closeDrawer = useCallback(() => {
        Animated.timing(drawerTranslateX, {
            toValue: -DRAWER_WIDTH,
            duration: 200,
            useNativeDriver: true,
        }).start(() => {
            setDrawerOpen(false)
        })
    }, [drawerTranslateX])

    const backdropOpacity = drawerTranslateX.interpolate({
        inputRange: [-DRAWER_WIDTH, 0],
        outputRange: [0, 0.6],
        extrapolate: 'clamp',
    })

    const panResponder = useRef(
        PanResponder.create({
            onStartShouldSetPanResponder: () => false,
            onMoveShouldSetPanResponder: (evt, gestureState) => {
                const { x0, dx, dy } = gestureState
                const isHorizontal = Math.abs(dx) > Math.abs(dy) * 1.5
                if (!drawerOpen) {
                    return x0 < 45 && dx > 8 && isHorizontal
                }
                return dx < -8 && isHorizontal
            },
            onPanResponderMove: (evt, gestureState) => {
                const { dx } = gestureState
                if (!drawerOpen) {
                    const val = -DRAWER_WIDTH + dx
                    drawerTranslateX.setValue(Math.min(val, 0))
                } else {
                    const val = dx
                    drawerTranslateX.setValue(Math.min(Math.max(-DRAWER_WIDTH, val), 0))
                }
            },
            onPanResponderRelease: (evt, gestureState) => {
                const { dx, vx } = gestureState
                if (!drawerOpen) {
                    if (dx > DRAWER_WIDTH * 0.4 || vx > 0.4) {
                        openDrawer()
                    } else {
                        closeDrawer()
                    }
                } else {
                    if (dx < -DRAWER_WIDTH * 0.4 || vx < -0.4) {
                        closeDrawer()
                    } else {
                        openDrawer()
                    }
                }
            },
        })
    ).current

    const api = useMemo(() => new CrushApi(connectedUrl), [connectedUrl])
    const handleDeleteSession = useCallback(
        async (id: string) => {
            try {
                // Optimistically update UI
                setSessions((prev) => prev.filter((s) => s.id !== id))
                setDeletingSessionId(null)

                await api.deleteSession(id)

                if (sessionIDRef.current === id) {
                    setSessions((currentSessions) => {
                        const aliveSorted = currentSessions
                            .filter((s) => s.id !== id && s.alive !== false)
                            .sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0))
                        if (aliveSorted.length > 0) {
                            setSessionID(aliveSorted[0].id)
                        } else if (currentSessions.length > 0) {
                            const fallback = currentSessions.find((s) => s.id !== id)
                            setSessionID(fallback?.id || '')
                        } else {
                            setSessionID('')
                        }
                        return currentSessions
                    })
                }
            } catch (err) {
                console.error('Failed to delete session', err)
            }
        },
        [api]
    )
    const handleQuickSwitchModel = useCallback(
        async (role: 'brain' | 'worker', provider: string, model: string) => {
            if (!api || !sessionID) return
            setSwitchingModelRole(role)
            setQuickSelectRole(null)
            try {
                await api.setModel(sessionID, role, provider, model)
            } catch (err) {
                console.error('Quick setModel failed', err)
            } finally {
                setSwitchingModelRole(null)
            }
        },
        [api, sessionID]
    )

    const handleCheckMobileUpdate = useCallback(async () => {
        setMobileUpdateStatus('checking')
        setMobileUpdateError('')
        setMobileUpdateProgress(0)
        try {
            const result = await checkForMobileUpdate()
            setMobileUpdateRelease(result.release)
            setMobileUpdateStatus(result.updateAvailable ? 'available' : 'latest')
        } catch (error) {
            console.error('Failed to check mobile update', error)
            setMobileUpdateError(describeError(error))
            setMobileUpdateStatus('error')
        }
    }, [])

    const handleInstallMobileUpdate = useCallback(async () => {
        const release = mobileUpdateRelease
        if (!release) {
            await handleCheckMobileUpdate()
            return
        }
        setMobileUpdateStatus('downloading')
        setMobileUpdateError('')
        setMobileUpdateProgress(0)
        try {
            await downloadAndOpenAndroidUpdate(release, (progressRatio) => {
                setMobileUpdateProgress(Math.max(0, Math.min(1, progressRatio)))
            })
            setMobileUpdateStatus('opening')
        } catch (error) {
            console.error('Failed to install mobile update', error)
            setMobileUpdateError(describeError(error))
            setMobileUpdateStatus('error')
        }
    }, [handleCheckMobileUpdate, mobileUpdateRelease])

    useEffect(() => {
        const timeout = setTimeout(() => {
            void handleCheckMobileUpdate()
        }, 1200)
        return () => clearTimeout(timeout)
    }, [handleCheckMobileUpdate])

    const unsubscribeRef = useRef<null | (() => void)>(null)
    const sessionsUnsubRef = useRef<null | (() => void)>(null)
    const cancelRequestedRef = useRef(false)

    const flatListRef = useRef<FlatList>(null)
    const isCloseToBottom = useRef(true)
    const [loadingOlder, setLoadingOlder] = useState(false)
    const [exhaustedHistory, setExhaustedHistory] = useState(false)
    const oldestLoadedTsRef = useRef<number>(0)
    const loadingOlderRef = useRef(false)
    // True iff the user is the one actively dragging the scroll view.
    // Lazy-load only fires on user-initiated scrolls so SSE-driven
    // scrollToEnd animations (which pass through y=0 briefly) cannot
    // trick us into pulling history.
    const userScrollRef = useRef(false)

    const handleScroll = useCallback((event: any) => {
        const { layoutMeasurement, contentOffset, contentSize } = event.nativeEvent
        const paddingToBottom = 80
        isCloseToBottom.current =
            layoutMeasurement.height + contentOffset.y >= contentSize.height - paddingToBottom
        // Lazy-load only when the user is actively dragging upward and we
        // are near the top. Auto scroll-to-end animations from SSE bursts
        // briefly cross y<200 too, which used to spam loadOlderHistory and
        // freeze isCloseToBottom to false, killing auto-scroll.
        if (userScrollRef.current && contentOffset.y < 200 && !loadingOlderRef.current) {
            void loadOlderHistoryRef.current?.()
        }
    }, [])

    const handleScrollBeginDrag = useCallback(() => {
        userScrollRef.current = true
    }, [])
    const handleMomentumScrollEnd = useCallback(() => {
        userScrollRef.current = false
    }, [])

    // Forward ref so loadOlderHistory (declared early) can call the live
    // handleEvent (declared later) without TDZ. Set on every render.
    const handleEventRef = useRef<((env: CrushEnvelope) => void) | null>(null)
    const loadOlderHistoryRef = useRef<(() => Promise<void>) | null>(null)

    // Infinite-scroll-up: pull another 30 min of older history into the
    // existing message Map when the user reaches the top of the list. The
    // Map auto-dedupes (so re-pulling never adds duplicates) and the flush
    // sorts by created_at so back-fills land in chronological position.
    const loadOlderHistory = useCallback(async () => {
        if (loadingOlderRef.current || exhaustedHistory) return
        if (!sessionID) return
        if (messagesMapRef.current.size === 0) return
        let oldestTs = Number.POSITIVE_INFINITY
        for (const m of messagesMapRef.current.values()) {
            const t = m.created_at || 0
            if (t > 0 && t < oldestTs) oldestTs = t
        }
        if (!isFinite(oldestTs)) return
        const oldestTsMs = oldestTs * 1000
        if (oldestLoadedTsRef.current && oldestTsMs >= oldestLoadedTsRef.current) {
            return
        }
        oldestLoadedTsRef.current = oldestTsMs
        loadingOlderRef.current = true
        setLoadingOlder(true)
        try {
            const sizeBefore = messagesMapRef.current.size
            await api.loadOlderHistory(sessionID, oldestTsMs, 30 * 60 * 1000, (env) => {
                try {
                    handleEventRef.current?.(env)
                } catch (err) {
                    console.log('loadOlderHistory event handler error:', err)
                }
            })
            const out = Array.from(messagesMapRef.current.values())
            out.sort((a, b) => (a.created_at || 0) - (b.created_at || 0))
            setMessages(out)
            if (messagesMapRef.current.size === sizeBefore) {
                setExhaustedHistory(true)
            }
        } catch (err) {
            console.log('loadOlderHistory failed:', err)
        } finally {
            loadingOlderRef.current = false
            setLoadingOlder(false)
        }
    }, [api, sessionID, exhaustedHistory])
    loadOlderHistoryRef.current = loadOlderHistory

    const handleContentSizeChange = useCallback(() => {
        if (isCloseToBottom.current) {
            flatListRef.current?.scrollToEnd({ animated: true })
        }
    }, [])

    // Cheap scroll trigger: last message id + part count. JSON.stringify on
    // the whole parts array would re-serialize every streaming token
    // (parts mutates each token), wasting CPU and forcing a sort.
    const lastMessageSig = useMemo(() => {
        if (messages.length === 0) return ''
        const last = messages[messages.length - 1]
        return `${last.id}:${last.parts?.length ?? 0}`
    }, [messages])

    useEffect(() => {
        if (isCloseToBottom.current) {
            const timer = setTimeout(() => {
                flatListRef.current?.scrollToEnd({ animated: true })
            }, 50)
            return () => clearTimeout(timer)
        }
    }, [lastMessageSig])

    const activeSession = useMemo(() => {
        return sessions.find((s) => s.id === sessionID)
    }, [sessions, sessionID])

    // Sync is_busy from the heartbeat ONLY when the active session changes.
    // Otherwise the 5s presence poll would race with the real-time
    // agent_event stream: stream sets is_busy=true on turn_started,
    // then 50-2000ms later the heartbeat (carrying a stale snapshot)
    // flips it back to false — visible as the send/stop button flicker.
    // agent_event is the source of truth while a session is open.
    useEffect(() => {
        if (activeSession) {
            setAgentInfo((prev) => ({ ...prev, is_busy: !!activeSession.is_busy }))
        }
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [activeSession?.id])

    const lastSyncedRef = useRef<{
        sessionId: string
        role: string
        provider: string
        model: string
    } | null>(null)

    // Sync selected role's current model/provider when activeSession or modelRole changes
    useEffect(() => {
        if (!activeSession) {
            setModelProvider('')
            setModelId('')
            lastSyncedRef.current = null
            return
        }

        const currentProvider =
            activeSession.models?.[modelRole]?.provider ||
            (modelRole === 'brain' ? activeSession.provider : '') ||
            ''
        const currentModel =
            activeSession.models?.[modelRole]?.model ||
            (modelRole === 'brain' ? activeSession.model : '') ||
            ''

        // Only overwrite if the active session ID changed, the selected role changed,
        // or the TUI actually updated to a new model for this role.
        const needsSync =
            !lastSyncedRef.current ||
            lastSyncedRef.current.sessionId !== activeSession.id ||
            lastSyncedRef.current.role !== modelRole ||
            lastSyncedRef.current.provider !== currentProvider ||
            lastSyncedRef.current.model !== currentModel

        if (needsSync) {
            setModelProvider(currentProvider)
            setModelId(currentModel)
            lastSyncedRef.current = {
                sessionId: activeSession.id,
                role: modelRole,
                provider: currentProvider,
                model: currentModel,
            }
        }
    }, [activeSession, modelRole])
    // Model chip text. Priority:
    //   1. live agent event (model_cfg.model or model.id) — most recent
    //   2. session presence record (session.model) — survives reconnect
    //   3. fallback when nothing is known yet
    const modelName =
        agentInfo.model_cfg?.model || agentInfo.model?.id || activeSession?.model || '未就绪'
    const brainModel = activeSession?.models?.brain?.model || activeSession?.model || '未就绪'
    const workerModel = activeSession?.models?.worker?.model || '未设定'

    const shortPath = (path: string) => {
        const parts = path.split('/').filter(Boolean)
        return parts.length > 2 ? parts.slice(-2).join('/') : path || '未注册'
    }

    const describeSession = useCallback(
        (session: Session) => {
            const rawTitle = (session.title || '').trim()
            const pathLabel = shortPath(session.path || '')
            const hasPath = pathLabel !== '未注册'
            const internalTitle = looksLikeInternalSessionTitle(rawTitle, session.id)
            const isSingleton = sessions.length === 1

            if (isSingleton) {
                return {
                    primary: hasPath ? pathLabel : rawTitle || '未命名会话',
                    secondary: !internalTitle && rawTitle && rawTitle !== pathLabel ? rawTitle : '',
                }
            }

            if (!internalTitle) {
                return {
                    primary: rawTitle,
                    secondary: hasPath && pathLabel !== rawTitle ? pathLabel : '',
                }
            }

            return {
                primary: hasPath ? pathLabel : '未命名会话',
                secondary: '',
            }
        },
        [sessions.length]
    )

    const activityStatusStyle = (statusName: ActivityEntry['status']) => {
        if (statusName === 'running') return styles.activity_running
        if (statusName === 'done') return styles.activity_done
        return styles.activity_failed
    }

    const recordActivity = useCallback((event: AgentEvent) => {
        if (!event.sub_agent_tool_call_id) return
        const status =
            event.type === 'sub_agent_failed'
                ? 'failed'
                : event.type === 'sub_agent_finished'
                  ? 'done'
                  : 'running'
        const entry: ActivityEntry = {
            id: event.sub_agent_tool_call_id,
            title: event.session_title || event.sub_agent_profile || 'sub-agent',
            profile: event.sub_agent_profile || 'worker',
            prompt: event.sub_agent_prompt || '',
            status: status,
            error: event.sub_agent_error,
            updatedAt: Date.now(),
        }
        setActivities((prev) => {
            const index = prev.findIndex((item) => item.id === entry.id)
            if (index < 0) return [entry, ...prev].slice(0, 20)
            const merged = [...prev]
            merged[index] = {
                ...merged[index],
                ...entry,
                prompt: entry.prompt || merged[index].prompt,
            }
            const [item] = merged.splice(index, 1)
            return [item, ...merged].slice(0, 20)
        })
        // Auto-remove finished/failed activities after 4 seconds so the bar
        // doesn't accumulate stale cards.
        if (status === 'done' || status === 'failed') {
            setTimeout(() => {
                setActivities((prev) => prev.filter((a) => a.id !== entry.id))
            }, 4000)
        }
    }, [])

    const handleEvent = useCallback(
        (envelope: CrushEnvelope) => {
            if (envelope.type === 'message') {
                const nextMessage = envelope.payload.payload
                if (nextMessage.session_id && nextMessage.session_id !== sessionIDRef.current) {
                    return
                }
                const map = messagesMapRef.current
                if (envelope.payload.type === 'deleted') {
                    if (!map.delete(nextMessage.id)) return
                } else {
                    // Map preserves insertion order; setting an existing key
                    // does NOT reorder, so streaming token updates land in
                    // place — same semantics as the old findIndex update.
                    if (nextMessage.role === 'assistant' && isMessageCanceled(nextMessage)) {
                        for (const id of Array.from(localCancelMessageIdsRef.current)) {
                            if (map.get(id)?.session_id === nextMessage.session_id) {
                                map.delete(id)
                                localCancelMessageIdsRef.current.delete(id)
                            }
                        }
                    }
                    map.set(nextMessage.id, nextMessage)
                    if (nextMessage.role === 'assistant' && isMessageFinished(nextMessage)) {
                        setAgentInfo((prev) => ({ ...prev, is_busy: false }))
                    }
                }
                if (!messagesFlushScheduledRef.current) {
                    messagesFlushScheduledRef.current = true
                    setTimeout(() => {
                        messagesFlushScheduledRef.current = false
                        const out = Array.from(messagesMapRef.current.values())
                        out.sort((a, b) => (a.created_at || 0) - (b.created_at || 0))
                        const g = globalThis as any
                        const isFirstFlush = !g.__perfFirstFlush && g.__perfTap
                        if (isFirstFlush) {
                            g.__perfFirstFlush = Date.now()
                            console.log(
                                `[PERF] T3 first_flush +${g.__perfFirstFlush - g.__perfTap}ms count=${out.length}`
                            )
                        }
                        setMessages(out)
                        // First flush after switching to a session with
                        // history: stick to bottom synchronously so the
                        // user sees the newest content, not the oldest.
                        if (isFirstFlush) {
                            isCloseToBottom.current = true
                            requestAnimationFrame(() => {
                                requestAnimationFrame(() => {
                                    flatListRef.current?.scrollToEnd({ animated: false })
                                })
                            })
                        }
                    }, 100)
                }
                return
            }
            if (envelope.type === 'agent_event') {
                const ev = envelope.payload.payload
                if (
                    ev.type === 'is_busy' ||
                    ev.type === 'turn_started' ||
                    ev.type === 'agent_started'
                ) {
                    setAgentInfo((prev) => ({ ...prev, is_busy: true }))
                } else if (
                    ev.type === 'agent_finished' ||
                    ev.type === 'is_idle' ||
                    ev.type === 'turn_finished' ||
                    ev.type === 'sub_agent_finished' ||
                    ev.type === 'sub_agent_failed'
                ) {
                    setAgentInfo((prev) => ({ ...prev, is_busy: false }))
                }
                return
            }
            if (envelope.type === 'permission_request') {
                const request = envelope.payload.payload
                setPendingPermissions((prev) => [
                    ...prev.filter((item) => item.tool_call_id !== request.tool_call_id),
                    request,
                ])
                return
            }
            if (envelope.type === 'permission_notification') {
                const toolCallID = envelope.payload.payload.tool_call_id
                setPendingPermissions((prev) =>
                    prev.filter((item) => item.tool_call_id !== toolCallID)
                )
            }
        },
        [recordActivity]
    )
    handleEventRef.current = handleEvent

    const connect = useCallback(async () => {
        setIsLoading(true)
        setErrorText('')
        try {
            const normalizedUrl = serverUrl.trim()
            setConnectedUrl(normalizedUrl)
            setStatus('连接中')
            setShowConnectionSettings(false)
        } catch (error) {
            console.error(error)
            setStatus('连接失败')
            setErrorText(error instanceof Error ? error.message : String(error))
            setShowConnectionSettings(true)
        } finally {
            setIsLoading(false)
        }
    }, [serverUrl])

    useEffect(() => {
        if (!searchServerUrl) return
        setServerUrl(searchServerUrl)
        setConnectedUrl(searchServerUrl)
        setStatus('连接中')
        setShowConnectionSettings(false)
        setErrorText('')
    }, [searchServerUrl])

    useEffect(() => {
        const applyDeepLink = async (rawUrl: string | null) => {
            const nextServerUrl = parseServerUrlFromDeepLink(rawUrl)
            if (!nextServerUrl) return
            setServerUrl(nextServerUrl)
            setConnectedUrl(nextServerUrl)
            setStatus('连接中')
            setShowConnectionSettings(false)
            setErrorText('')
        }

        void Linking.getInitialURL().then(applyDeepLink)
        const subscription = Linking.addEventListener('url', (event) => {
            void applyDeepLink(event.url)
        })
        return () => subscription.remove()
    }, [])

    // Live KV watch for sessions list. Cleans up + resubscribes on api change.
    useEffect(() => {
        let active = true
        sessionsUnsubRef.current?.()
        sessionsUnsubRef.current = null
        setStatus('连接中')
        ;(async () => {
            try {
                const unsub = await api.listSessions((next) => {
                    if (!active) return
                    console.log(
                        `[PERF] listSessions cb: ${next.length} sessions, alive=${next.filter((s) => s.alive !== false).length}, first=${next[0]?.id?.slice(0, 8)}/${next[0]?.alive}`
                    )
                    setSessions(next)
                    setStatus('在线')
                    setSessionID((prev) => {
                        const aliveSorted = next
                            .filter((s) => s.alive !== false)
                            .sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0))
                        if (prev && aliveSorted.some((s) => s.id === prev)) {
                            return prev
                        }
                        const picked = aliveSorted[0]?.id || next[0]?.id || ''
                        console.log(
                            `[PERF] setSessionID prev=${prev?.slice(0, 8) || '(empty)'} -> ${picked?.slice(0, 8) || '(empty)'}`
                        )
                        return picked
                    })
                })
                if (!active) {
                    try {
                        unsub()
                    } catch {
                        // ignore
                    }
                    return
                }
                sessionsUnsubRef.current = unsub
            } catch (error) {
                console.log('Failed to subscribe sessions:', error)
                if (active) {
                    setStatus('连接失败')
                    setErrorText(error instanceof Error ? error.message : String(error))
                }
            }
        })()

        return () => {
            active = false
            try {
                sessionsUnsubRef.current?.()
            } catch {
                // ignore
            }
            sessionsUnsubRef.current = null
        }
    }, [api])

    // Subscribe to per-session events whenever sessionID changes.
    useEffect(() => {
        let active = true
        try {
            unsubscribeRef.current?.()
        } catch {
            // ignore
        }
        unsubscribeRef.current = null

        if (!sessionID) {
            messagesMapRef.current.clear()
            setMessages([])
            return
        }

        // PERF: T0 = sessionID-change moment.
        const g = globalThis as any
        g.__perfTap = Date.now()
        g.__perfFirstEvent = 0
        g.__perfFirstFlush = 0
        g.__perfRendered = 0
        console.log(`[PERF] T0 session_change session=${sessionID}`)

        // Events replay full history via deliverPolicy: 'all'; start fresh.
        messagesMapRef.current.clear()
        localCancelMessageIdsRef.current.clear()
        setMessages([])
        // Sync ref BEFORE subscribe so the first envelope (which may
        // arrive in <50ms, before the next React commit) doesn't get
        // filtered out by handleEvent's sessionIDRef.current check.
        sessionIDRef.current = sessionID
        oldestLoadedTsRef.current = 0
        setExhaustedHistory(false)
        userScrollRef.current = false
        loadingOlderRef.current = false
        ;(async () => {
            try {
                console.log(`[PERF] T1 subscribe_begin +${Date.now() - g.__perfTap}ms`)
                const unsub = await api.subscribeSessionEvents(
                    sessionID,
                    (envelope) => {
                        if (!active) return
                        const g2 = globalThis as any
                        if (!g2.__perfFirstEvent && g2.__perfTap) {
                            g2.__perfFirstEvent = Date.now()
                            console.log(
                                `[PERF] T2 first_event +${g2.__perfFirstEvent - g2.__perfTap}ms type=${envelope.type}`
                            )
                        }
                        setStatus('在线')
                        handleEvent(envelope)
                    },
                    (err) => {
                        console.log('Crush events failed:', err)
                        if (active) setStatus('连接失败')
                    }
                )
                if (!active) {
                    try {
                        unsub()
                    } catch {
                        // ignore
                    }
                    return
                }
                unsubscribeRef.current = unsub
            } catch (error) {
                console.log('Failed to subscribe session events:', error)
                if (active) {
                    setStatus('连接失败')
                    setErrorText(error instanceof Error ? error.message : String(error))
                }
            }
        })()

        return () => {
            active = false
            try {
                unsubscribeRef.current?.()
            } catch {
                // ignore
            }
            unsubscribeRef.current = null
        }
    }, [api, sessionID, handleEvent])

    const sendMessage = async () => {
        const prompt = input.trim()
        if (!prompt || !sessionID || sendingRef.current) return
        sendingRef.current = true
        try {
            cancelRequestedRef.current = false
            setInput('')
            // Push to history dedup-against-last so spam-Enter doesn't fill
            // the stack. Reset cursor and draft so the next ↑ lands on this
            // freshly-sent entry.
            setInputHistory((prev) => (prev[prev.length - 1] === prompt ? prev : [...prev, prompt]))
            setHistoryIndex(-1)
            inputDraftRef.current = ''
            setErrorText('')

            isCloseToBottom.current = true
            setTimeout(() => {
                flatListRef.current?.scrollToEnd({ animated: true })
            }, 50)

            await api.sendMessage(sessionID, prompt)
            setAgentInfo((prev) => ({ ...prev, is_busy: true }))
        } catch (error) {
            if (cancelRequestedRef.current) return
            console.error(error)
            setInput(prompt)
            setErrorText(error instanceof Error ? error.message : String(error))
        } finally {
            sendingRef.current = false
        }
    }

    const createNewSession = async () => {
        // New NATS API surfaces sessions via KV watch; creation is server-side only.
        setErrorText('新会话需在 Crush 服务端创建（NATS KV 自动同步）')
    }

    const cancelRun = async () => {
        if (!sessionID) return
        cancelRequestedRef.current = true
        setErrorText('')
        try {
            await api.cancelSession(sessionID)
            const nowMs = Date.now()
            const localCancelID = `local-cancel-${sessionID}-${nowMs}`
            const localCancelMessage: Message = {
                id: localCancelID,
                role: 'assistant',
                session_id: sessionID,
                created_at: nowMs / 1000,
                parts: [
                    {
                        type: 'finish',
                        data: {
                            reason: 'canceled',
                            message: 'Canceled',
                            time: Math.floor(nowMs / 1000),
                        },
                    },
                ],
            }
            localCancelMessageIdsRef.current.add(localCancelID)
            messagesMapRef.current.set(localCancelID, localCancelMessage)
            const out = Array.from(messagesMapRef.current.values())
            out.sort((a, b) => (a.created_at || 0) - (b.created_at || 0))
            setMessages(out)
            isCloseToBottom.current = true
            requestAnimationFrame(() => {
                flatListRef.current?.scrollToEnd({ animated: true })
            })
            setAgentInfo((prev) => ({ ...prev, is_busy: false }))
        } catch (error) {
            console.error(error)
            setErrorText(error instanceof Error ? error.message : String(error))
        }
    }

    const grantPermission = async (
        permission: PermissionRequest,
        action: 'allow' | 'allow_session' | 'deny'
    ) => {
        if (!sessionID) return
        try {
            const nextAction: 'allow' | 'deny' = action === 'deny' ? 'deny' : 'allow'
            await api.grantPermission(sessionID, permission.tool_call_id, nextAction)
            setPendingPermissions((prev) =>
                prev.filter((item) => item.tool_call_id !== permission.tool_call_id)
            )
        } catch (error) {
            console.error(error)
            setErrorText(error instanceof Error ? error.message : String(error))
        }
    }

    const displayMessages = useMemo(() => {
        const getMessageRank = (msg: Message): number => {
            if (msg.role === 'user') return 1
            if (msg.role === 'assistant') {
                const hasToolOrReasoning = msg.parts?.some(
                    (p) => p.type === 'tool_call' || p.type === 'reasoning'
                )
                return hasToolOrReasoning ? 2 : 4
            }
            if (msg.role === 'tool') return 3
            return 5
        }

        const sorted = [...messages]
            .map((msg, idx) => ({ msg, idx }))
            .sort((a, b) => {
                const timeA = a.msg.created_at ?? 0
                const timeB = b.msg.created_at ?? 0
                if (timeA !== timeB) {
                    return timeA - timeB
                }
                const rankA = getMessageRank(a.msg)
                const rankB = getMessageRank(b.msg)
                if (rankA !== rankB) {
                    return rankA - rankB
                }
                return a.idx - b.idx
            })
            .map((item) => item.msg)

        let baseMessages = sorted
        if (activeRunVisible && sorted.length > 0 && sorted[sorted.length - 1].role === 'user') {
            baseMessages = [
                ...sorted,
                {
                    id: 'typing-indicator',
                    role: 'assistant',
                    parts: [],
                } as unknown as Message,
            ]
        }

        const aggregated: Message[] = []
        for (const msg of baseMessages) {
            if (msg.role === 'tool') {
                let merged = false
                for (let i = aggregated.length - 1; i >= 0; i--) {
                    if (
                        aggregated[i].role === 'assistant' &&
                        aggregated[i].id !== 'typing-indicator'
                    ) {
                        aggregated[i].parts = [...(aggregated[i].parts || []), ...(msg.parts || [])]
                        merged = true
                        break
                    }
                }
                if (merged) continue
            } else if (msg.role === 'assistant' && msg.id !== 'typing-indicator') {
                let merged = false
                for (let i = aggregated.length - 1; i >= 0; i--) {
                    if (aggregated[i].role === 'user') {
                        break
                    }
                    if (
                        aggregated[i].role === 'assistant' &&
                        aggregated[i].id !== 'typing-indicator'
                    ) {
                        aggregated[i].parts = [...(aggregated[i].parts || []), ...(msg.parts || [])]
                        merged = true
                        break
                    }
                }
                if (merged) continue
            }

            aggregated.push({
                ...msg,
                parts: msg.parts ? [...msg.parts] : [],
            })
        }
        return aggregated.filter(messageHasVisibleContent)
    }, [messages, activeRunVisible])

    const isConnected = status === '在线'
    const nativeVersion = Application.nativeApplicationVersion || '0.0.0'
    const nativeBuildVersion = Application.nativeBuildVersion || '0'
    const mobileUpdateButtonDisabled =
        mobileUpdateStatus === 'checking' ||
        mobileUpdateStatus === 'downloading' ||
        mobileUpdateStatus === 'opening'
    const mobileUpdateButtonText =
        mobileUpdateStatus === 'checking'
            ? '检查中'
            : mobileUpdateStatus === 'downloading'
              ? `${Math.round(mobileUpdateProgress * 100)}%`
              : mobileUpdateStatus === 'opening'
                ? '安装器'
                : mobileUpdateStatus === 'available'
                  ? '安装'
                  : '检查'
    const mobileUpdateStateText =
        mobileUpdateStatus === 'available' && mobileUpdateRelease
            ? `发现 v${mobileUpdateRelease.version}`
            : mobileUpdateStatus === 'latest'
              ? '已是最新'
              : mobileUpdateStatus === 'error'
                ? mobileUpdateError || '更新检查失败'
                : mobileUpdateStatus === 'downloading'
                  ? `下载 ${Math.round(mobileUpdateProgress * 100)}%`
                  : mobileUpdateStatus === 'opening'
                    ? '已打开安装器'
                    : '等待检查'

    return (
        <View
            style={[
                styles.safeArea,
                {
                    paddingTop: insets.top,
                    paddingBottom: insets.bottom,
                },
            ]}
            {...panResponder.panHandlers}>
            <KeyboardAvoidingView
                behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
                keyboardVerticalOffset={Platform.OS === 'ios' ? 0 : 54}
                style={styles.root}>
                <View style={styles.header}>
                    <View style={styles.headerTop}>
                        <Pressable
                            style={({ pressed }) => [
                                styles.titleContainer,
                                pressed && styles.pressed,
                            ]}
                            onPress={openDrawer}>
                            <Feather
                                name="menu"
                                size={18}
                                color="#f8fafc"
                                style={{ marginRight: 2 }}
                            />
                            <Text style={styles.title}>Crush</Text>
                            {activeSession?.path ? (
                                <Text
                                    style={styles.titlePath}
                                    numberOfLines={1}
                                    ellipsizeMode="tail">
                                    {shortPath(activeSession.path)}
                                </Text>
                            ) : null}
                            <Animated.View
                                style={[
                                    styles.statusDot,
                                    isConnected ? styles.statusDotOnline : styles.statusDotOffline,
                                    {
                                        opacity: isConnected
                                            ? activeRunVisible
                                                ? blinkAnim
                                                : 1.0
                                            : 1.0,
                                    },
                                ]}
                            />
                            {activeRunVisible && isAgentReasoning ? (
                                <View
                                    style={{
                                        flexDirection: 'row',
                                        alignItems: 'center',
                                        columnGap: 3,
                                    }}>
                                    <Text style={styles.statusText}>正在思考... </Text>
                                    <BrailleSpinner color="#64748b" size={11} />
                                </View>
                            ) : (
                                <Text style={styles.statusText}>
                                    {activeRunVisible ? '运行中... ⚙️' : status}
                                </Text>
                            )}
                        </Pressable>
                    </View>

                    {showConnectionSettings && (
                        <View style={styles.serverPanel}>
                            <View style={styles.serverRow}>
                                <TextInput
                                    value={serverUrl}
                                    onChangeText={setServerUrl}
                                    autoCapitalize="none"
                                    autoCorrect={false}
                                    style={styles.serverInput}
                                    placeholder="ws://host:port"
                                    placeholderTextColor="#4b5563"
                                />
                                <Pressable
                                    style={({ pressed }) => [
                                        styles.connectButton,
                                        pressed && styles.pressed,
                                    ]}
                                    onPress={connect}>
                                    {isLoading ? (
                                        <ActivityIndicator color="#ffffff" size="small" />
                                    ) : (
                                        <Text style={styles.buttonText}>连接</Text>
                                    )}
                                </Pressable>
                            </View>
                            {errorText ? <Text style={styles.errorText}>{errorText}</Text> : null}

                            {sessionID ? (
                                <View style={styles.modelPicker}>
                                    <Text style={styles.modelPickerLabel}>
                                        切换模型 (写入 state.yaml)
                                    </Text>
                                    <View style={styles.modelRoleRow}>
                                        {(['brain', 'worker', 'explore'] as const).map((r) => (
                                            <Pressable
                                                key={r}
                                                onPress={() => setModelRole(r)}
                                                style={[
                                                    styles.modelRoleChip,
                                                    modelRole === r && styles.modelRoleChipActive,
                                                ]}>
                                                <Text
                                                    style={[
                                                        styles.modelRoleChipText,
                                                        modelRole === r &&
                                                            styles.modelRoleChipTextActive,
                                                    ]}>
                                                    {r}
                                                </Text>
                                            </Pressable>
                                        ))}
                                    </View>
                                    {activeSession?.available_models &&
                                    activeSession.available_models.length > 0 ? (
                                        <View style={styles.availableModelsList}>
                                            <ScrollView
                                                style={styles.modelsScrollView}
                                                nestedScrollEnabled={true}>
                                                {activeSession.available_models.map((m) => {
                                                    const isSelected =
                                                        modelProvider === m.provider &&
                                                        modelId === m.model
                                                    return (
                                                        <Pressable
                                                            key={`${m.provider}/${m.model}`}
                                                            onPress={() => {
                                                                setModelProvider(m.provider)
                                                                setModelId(m.model)
                                                            }}
                                                            style={[
                                                                styles.modelOptionRow,
                                                                isSelected &&
                                                                    styles.modelOptionRowActive,
                                                            ]}>
                                                            <View style={styles.modelOptionInfo}>
                                                                <Text
                                                                    style={[
                                                                        styles.modelOptionProvider,
                                                                        isSelected &&
                                                                            styles.modelOptionTextActive,
                                                                    ]}>
                                                                    {m.provider}
                                                                </Text>
                                                                <Text
                                                                    style={[
                                                                        styles.modelOptionName,
                                                                        isSelected &&
                                                                            styles.modelOptionTextActive,
                                                                    ]}>
                                                                    {m.model}
                                                                </Text>
                                                            </View>
                                                            {isSelected && (
                                                                <Feather
                                                                    name="check"
                                                                    size={14}
                                                                    color="#38bdf8"
                                                                />
                                                            )}
                                                        </Pressable>
                                                    )
                                                })}
                                            </ScrollView>
                                        </View>
                                    ) : null}
                                    <TextInput
                                        value={modelProvider}
                                        onChangeText={setModelProvider}
                                        autoCapitalize="none"
                                        autoCorrect={false}
                                        style={[
                                            styles.serverInput,
                                            { flex: 0, width: '100%', paddingVertical: 0 },
                                        ]}
                                        placeholder="provider id (例 wecode)"
                                        placeholderTextColor="#4b5563"
                                    />
                                    <TextInput
                                        value={modelId}
                                        onChangeText={setModelId}
                                        autoCapitalize="none"
                                        autoCorrect={false}
                                        style={[
                                            styles.serverInput,
                                            { flex: 0, width: '100%', paddingVertical: 0 },
                                        ]}
                                        placeholder="model id (例 gpt-5.4-mini)"
                                        placeholderTextColor="#4b5563"
                                    />
                                    <Pressable
                                        disabled={
                                            modelSaveStatus === 'saving' ||
                                            !modelProvider.trim() ||
                                            !modelId.trim()
                                        }
                                        style={({ pressed }) => [
                                            styles.connectButton,
                                            pressed && styles.pressed,
                                            (modelSaveStatus === 'saving' ||
                                                !modelProvider.trim() ||
                                                !modelId.trim()) &&
                                                styles.modelSaveDisabled,
                                        ]}
                                        onPress={async () => {
                                            if (!api || !sessionID) return
                                            setModelSaveStatus('saving')
                                            try {
                                                await api.setModel(
                                                    sessionID,
                                                    modelRole,
                                                    modelProvider.trim(),
                                                    modelId.trim()
                                                )
                                                setModelSaveStatus('saved')
                                                setTimeout(() => setModelSaveStatus('idle'), 2500)
                                            } catch (err) {
                                                console.error('setModel failed', err)
                                                setModelSaveStatus('error')
                                                setTimeout(() => setModelSaveStatus('idle'), 4000)
                                            }
                                        }}>
                                        <Text style={styles.buttonText}>
                                            {modelSaveStatus === 'saving'
                                                ? '保存中…'
                                                : modelSaveStatus === 'saved'
                                                  ? '已写入 state.yaml ✓'
                                                  : modelSaveStatus === 'error'
                                                    ? '失败 - 重试'
                                                    : `应用到 ${modelRole}`}
                                        </Text>
                                    </Pressable>
                                </View>
                            ) : null}
                        </View>
                    )}

                    <View style={styles.compactMetaRow}>
                        <Pressable
                            style={styles.metaBadge}
                            onPress={() => setQuickSelectRole('brain')}>
                            {switchingModelRole === 'brain' ? (
                                <ActivityIndicator size={11} color="#c084fc" />
                            ) : (
                                <Feather name="cpu" size={11} color="#c084fc" />
                            )}
                            <Text style={styles.metaBadgeText} numberOfLines={1}>
                                Brain: {brainModel}
                            </Text>
                        </Pressable>
                        <Pressable
                            style={styles.metaBadge}
                            onPress={() => setQuickSelectRole('worker')}>
                            {switchingModelRole === 'worker' ? (
                                <ActivityIndicator size={11} color="#eab308" />
                            ) : (
                                <Feather name="user" size={11} color="#eab308" />
                            )}
                            <Text style={styles.metaBadgeText} numberOfLines={1}>
                                Worker: {workerModel}
                            </Text>
                        </Pressable>
                    </View>
                </View>

                <View style={styles.content}>
                    <FlatList
                        ref={flatListRef}
                        style={styles.messages}
                        contentContainerStyle={styles.messagesContent}
                        data={displayMessages}
                        keyExtractor={(item) => item.id}
                        onScroll={handleScroll}
                        onScrollBeginDrag={handleScrollBeginDrag}
                        onMomentumScrollEnd={handleMomentumScrollEnd}
                        scrollEventThrottle={16}
                        onContentSizeChange={handleContentSizeChange}
                        initialNumToRender={8}
                        maxToRenderPerBatch={5}
                        windowSize={5}
                        removeClippedSubviews={true}
                        updateCellsBatchingPeriod={50}
                        ListHeaderComponent={
                            displayMessages.length === 0 ? null : exhaustedHistory ? (
                                <Text style={styles.loadOlderText}>—— 没有更早的消息 ——</Text>
                            ) : loadingOlder ? (
                                <Text style={styles.loadOlderText}>加载更早...</Text>
                            ) : (
                                <Text style={styles.loadOlderText}>向上滑动加载更早</Text>
                            )
                        }
                        renderItem={({ item, index }) => {
                            const prevMessage = index > 0 ? displayMessages[index - 1] : null
                            const isAgent = (role: string) => role !== 'user'
                            const showHeader =
                                !prevMessage || isAgent(prevMessage.role) !== isAgent(item.role)
                            return (
                                <MessageItem
                                    message={item}
                                    isUser={item.role === 'user'}
                                    showHeader={showHeader}
                                    isBusy={
                                        activeRunVisible && index === displayMessages.length - 1
                                    }
                                    onMaximize={handleMaximizeTerminal}
                                />
                            )
                        }}
                        ListEmptyComponent={
                            <View style={styles.emptyState}>
                                <Feather name="message-circle" size={40} color="#374151" />
                                <Text style={styles.emptyTitle}>
                                    {sessionID ? '等待输入任务...' : '等待会话注册'}
                                </Text>
                                <Text style={styles.emptyText}>
                                    连接成功后即可发送任务，Crush 将自主调用工具完成开发。
                                </Text>
                            </View>
                        }
                    />
                </View>

                {pendingPermissions.length > 0 && (
                    <View style={styles.permissionPanel}>
                        {pendingPermissions.slice(0, 2).map((permission) => (
                            <View key={permission.tool_call_id} style={styles.permissionCard}>
                                <View style={styles.permissionHeader}>
                                    <Feather name="shield" size={14} color="#fbbf24" />
                                    <Text style={styles.permissionTitle}>
                                        安全授权: {permission.tool_name}
                                    </Text>
                                </View>
                                <Text style={styles.permissionText} numberOfLines={2}>
                                    {permission.description || permission.path || '需要授权以执行'}
                                </Text>
                                <View style={styles.permissionActions}>
                                    <Pressable
                                        style={({ pressed }) => [
                                            styles.allowButton,
                                            pressed && styles.pressed,
                                        ]}
                                        onPress={() =>
                                            grantPermission(permission, 'allow_session')
                                        }>
                                        <Text style={styles.buttonText}>允许</Text>
                                    </Pressable>
                                    <Pressable
                                        style={({ pressed }) => [
                                            styles.denyButton,
                                            pressed && styles.pressed,
                                        ]}
                                        onPress={() => grantPermission(permission, 'deny')}>
                                        <Text style={styles.buttonText}>拒绝</Text>
                                    </Pressable>
                                </View>
                            </View>
                        ))}
                    </View>
                )}

                <View style={styles.inputBar}>
                    <TextInput
                        value={input}
                        onChangeText={setInput}
                        multiline
                        returnKeyType="send"
                        blurOnSubmit={true}
                        onSubmitEditing={() => {
                            if (input.trim()) {
                                sendMessage()
                            }
                        }}
                        autoCapitalize="none"
                        autoCorrect={false}
                        autoComplete="off"
                        keyboardType="default"
                        spellCheck={false}
                        disableFullscreenUI
                        importantForAutofill="no"
                        textContentType="none"
                        style={styles.promptInput}
                        placeholder={sessionID ? '输入任务或指令...' : '请先连接 Crush'}
                        placeholderTextColor="#4b5563"
                        onKeyPress={(e) => {
                            // zsh-style history walk on a hardware/Bluetooth
                            // keyboard (scrcpy passes through too). Only fires
                            // when there is no embedded newline in input — a
                            // multi-line draft keeps the native cursor-move
                            // behaviour because hijacking would break editing.
                            const key = (e.nativeEvent as { key?: string }).key
                            if (key !== 'ArrowUp' && key !== 'ArrowDown') return
                            if (input.includes('\n')) return
                            if (inputHistory.length === 0) return
                            e.preventDefault?.()
                            if (key === 'ArrowUp') {
                                // Stash the current draft on the first step
                                // back so a future ArrowDown past the newest
                                // entry restores it.
                                if (historyIndex === -1) inputDraftRef.current = input
                                const next =
                                    historyIndex === -1
                                        ? inputHistory.length - 1
                                        : Math.max(0, historyIndex - 1)
                                setHistoryIndex(next)
                                setInput(inputHistory[next])
                            } else {
                                // ArrowDown
                                if (historyIndex === -1) return
                                const next = historyIndex + 1
                                if (next >= inputHistory.length) {
                                    setHistoryIndex(-1)
                                    setInput(inputDraftRef.current)
                                } else {
                                    setHistoryIndex(next)
                                    setInput(inputHistory[next])
                                }
                            }
                        }}
                    />
                    {activeRunVisible ? (
                        <Pressable
                            style={({ pressed }) => [styles.stopButton, pressed && styles.pressed]}
                            onPress={cancelRun}>
                            <Feather name="square" size={14} color="#ffffff" />
                        </Pressable>
                    ) : (
                        <Pressable
                            style={({ pressed }) => [
                                styles.sendButton,
                                (!input.trim() || !sessionID) && styles.disabledButton,
                                pressed && styles.pressed,
                            ]}
                            disabled={!input.trim() || !sessionID}
                            onPress={sendMessage}>
                            <Feather name="send" size={14} color="#ffffff" />
                        </Pressable>
                    )}
                </View>

                {/* Drawer 阴影遮罩层 */}
                {drawerOpen && (
                    <Animated.View style={[styles.drawerBackdrop, { opacity: backdropOpacity }]}>
                        <Pressable style={{ flex: 1 }} onPress={closeDrawer} />
                    </Animated.View>
                )}

                {/* 左侧侧滑抽屉面板 */}
                <Animated.View
                    style={[
                        styles.drawerContainer,
                        { transform: [{ translateX: drawerTranslateX }] },
                    ]}>
                    <View style={styles.drawerHeader}>
                        <View style={styles.drawerHeaderTitleRow}>
                            <Text style={styles.drawerHeaderTitle}>Crush</Text>
                            <Animated.View
                                style={[
                                    styles.statusDot,
                                    isConnected ? styles.statusDotOnline : styles.statusDotOffline,
                                    {
                                        opacity: isConnected
                                            ? activeRunVisible
                                                ? blinkAnim
                                                : 1.0
                                            : 1.0,
                                    },
                                ]}
                            />
                        </View>
                        <Text style={styles.drawerHeaderSub} numberOfLines={1}>
                            服务地址: {connectedUrl}
                        </Text>
                        {activeSession && (
                            <View style={styles.drawerHeaderActiveSession}>
                                <Feather name="message-square" size={11} color="#10b981" />
                                <Text
                                    style={styles.drawerHeaderActiveSessionText}
                                    numberOfLines={1}>
                                    当前会话: {describeSession(activeSession).primary}
                                </Text>
                            </View>
                        )}
                    </View>

                    <ScrollView
                        ref={drawerScrollViewRef}
                        style={styles.drawerScroll}
                        showsVerticalScrollIndicator={false}>
                        <Text style={styles.drawerSectionTitle}>会话列表</Text>

                        {sessions.length === 0 ? (
                            <View style={{ paddingHorizontal: 20, paddingVertical: 10 }}>
                                <Text style={{ color: '#4b5563', fontSize: 12 }}>无会话</Text>
                            </View>
                        ) : (
                            (() => {
                                // Group sessions by cwd so multiple sessions
                                // under the same workspace collapse into one
                                // expandable header (two-level drawer).
                                const sortedSessions = [...sessions].sort((a, b) => {
                                    // Stable order: user-touched access time wins;
                                    // otherwise fall back to created_at (set once)
                                    // not updated_at (rewritten every heartbeat).
                                    const timeA =
                                        sessionAccessTimes[a.id] ||
                                        a.created_at ||
                                        a.updated_at ||
                                        0
                                    const timeB =
                                        sessionAccessTimes[b.id] ||
                                        b.created_at ||
                                        b.updated_at ||
                                        0
                                    if (timeB !== timeA) return timeB - timeA
                                    return (a.id || '').localeCompare(b.id || '')
                                })
                                const groups = new Map<string, typeof sessions>()
                                for (const s of sortedSessions) {
                                    if (!s || !s.id) continue
                                    const key = s.path || '(unknown)'
                                    const arr = groups.get(key)
                                    if (arr) arr.push(s)
                                    else groups.set(key, [s])
                                }
                                return Array.from(groups.entries()).map(([cwd, groupSessions]) => {
                                    if (groupSessions.length === 1) {
                                        const session = groupSessions[0]
                                        const isActiveSession = session.id === sessionID
                                        const isSessionBusy = isActiveSession
                                            ? activeRunVisible
                                            : !!session.is_busy
                                        const isSessionOnline =
                                            isConnected && session.alive !== false
                                        const display = describeSession(session)
                                        return (
                                            <Pressable
                                                key={session.id}
                                                style={({ pressed }) => [
                                                    styles.sessionNode,
                                                    isActiveSession && styles.sessionNodeActive,
                                                    pressed && styles.sessionNodePressed,
                                                ]}
                                                onPress={() => {
                                                    if (deletingSessionId) {
                                                        setDeletingSessionId(null)
                                                    } else {
                                                        setSessionID(session.id)
                                                        setSessionAccessTimes((prev) => ({
                                                            ...prev,
                                                            [session.id]: Date.now(),
                                                        }))
                                                        closeDrawer()
                                                    }
                                                }}
                                                onLongPress={() => {
                                                    setDeletingSessionId(session.id)
                                                }}>
                                                <View style={styles.sessionNodeBody}>
                                                    <View
                                                        style={{
                                                            flexDirection: 'row',
                                                            alignItems: 'center',
                                                            columnGap: 8,
                                                        }}>
                                                        <Animated.View
                                                            style={[
                                                                styles.statusDot,
                                                                isSessionOnline
                                                                    ? styles.statusDotOnline
                                                                    : styles.statusDotOffline,
                                                                {
                                                                    opacity: isSessionOnline
                                                                        ? isSessionBusy
                                                                            ? blinkAnim
                                                                            : 1.0
                                                                        : 1.0,
                                                                },
                                                            ]}
                                                        />
                                                        <Feather
                                                            name="message-square"
                                                            size={11}
                                                            color={
                                                                isActiveSession
                                                                    ? '#38bdf8'
                                                                    : '#4b5563'
                                                            }
                                                        />
                                                        <View
                                                            style={{
                                                                flexDirection: 'column',
                                                                flex: 1,
                                                            }}>
                                                            <Text
                                                                style={[
                                                                    styles.sessionNodeText,
                                                                    isActiveSession &&
                                                                        styles.sessionNodeTextActive,
                                                                ]}
                                                                numberOfLines={1}>
                                                                {display.primary}
                                                            </Text>
                                                            {display.secondary ? (
                                                                <Text
                                                                    style={
                                                                        styles.sessionNodeSubtext
                                                                    }
                                                                    numberOfLines={1}>
                                                                    {display.secondary}
                                                                </Text>
                                                            ) : null}
                                                        </View>
                                                    </View>
                                                </View>
                                                {deletingSessionId === session.id ? (
                                                    <View
                                                        style={{
                                                            flexDirection: 'row',
                                                            alignItems: 'center',
                                                            columnGap: 6,
                                                        }}>
                                                        <Pressable
                                                            style={({ pressed }) => [
                                                                styles.deleteConfirmBtn,
                                                                pressed && { opacity: 0.7 },
                                                            ]}
                                                            onPress={async (e) => {
                                                                e.stopPropagation()
                                                                await handleDeleteSession(
                                                                    session.id
                                                                )
                                                            }}>
                                                            <Text style={styles.deleteConfirmText}>
                                                                移除
                                                            </Text>
                                                        </Pressable>
                                                        <Pressable
                                                            style={({ pressed }) => [
                                                                styles.deleteCancelBtn,
                                                                pressed && { opacity: 0.7 },
                                                            ]}
                                                            onPress={(e) => {
                                                                e.stopPropagation()
                                                                setDeletingSessionId(null)
                                                            }}>
                                                            <Feather
                                                                name="x"
                                                                size={14}
                                                                color="#94a3b8"
                                                            />
                                                        </Pressable>
                                                    </View>
                                                ) : (
                                                    isActiveSession && (
                                                        <Feather
                                                            name="check"
                                                            size={12}
                                                            color="#38bdf8"
                                                        />
                                                    )
                                                )}
                                            </Pressable>
                                        )
                                    }

                                    const isCollapsed = collapsedCwdGroups.has(cwd)
                                    const containsActive = groupSessions.some(
                                        (s) => s.id === sessionID
                                    )
                                    const aliveCount = groupSessions.filter(
                                        (s) => s.alive !== false
                                    ).length
                                    return (
                                        <View key={cwd} style={styles.cwdGroup}>
                                            <Pressable
                                                style={({ pressed }) => [
                                                    styles.cwdGroupHeader,
                                                    containsActive && styles.cwdGroupHeaderActive,
                                                    pressed && styles.sessionNodePressed,
                                                ]}
                                                onPress={() => {
                                                    setCollapsedCwdGroups((prev) => {
                                                        const next = new Set(prev)
                                                        if (next.has(cwd)) next.delete(cwd)
                                                        else next.add(cwd)
                                                        return next
                                                    })
                                                }}>
                                                <Feather
                                                    name={
                                                        isCollapsed
                                                            ? 'chevron-right'
                                                            : 'chevron-down'
                                                    }
                                                    size={12}
                                                    color="#94a3b8"
                                                />
                                                <Feather
                                                    name="folder"
                                                    size={11}
                                                    color={containsActive ? '#38bdf8' : '#94a3b8'}
                                                />
                                                <Text
                                                    style={[
                                                        styles.cwdGroupTitle,
                                                        containsActive &&
                                                            styles.cwdGroupTitleActive,
                                                    ]}
                                                    numberOfLines={1}>
                                                    {shortPath(cwd)}
                                                </Text>
                                                <Text style={styles.cwdGroupCount}>
                                                    {aliveCount}/{groupSessions.length}
                                                </Text>
                                            </Pressable>
                                            {!isCollapsed &&
                                                groupSessions.map((session) => {
                                                    const isActiveSession = session.id === sessionID
                                                    const isSessionBusy = isActiveSession
                                                        ? activeRunVisible
                                                        : !!session.is_busy
                                                    const isSessionOnline =
                                                        isConnected && session.alive !== false
                                                    const display = describeSession(session)
                                                    return (
                                                        <Pressable
                                                            key={session.id}
                                                            style={({ pressed }) => [
                                                                styles.sessionNode,
                                                                styles.sessionNodeIndented,
                                                                isActiveSession &&
                                                                    styles.sessionNodeActive,
                                                                pressed &&
                                                                    styles.sessionNodePressed,
                                                            ]}
                                                            onPress={() => {
                                                                if (deletingSessionId) {
                                                                    setDeletingSessionId(null)
                                                                } else {
                                                                    setSessionID(session.id)
                                                                    setSessionAccessTimes(
                                                                        (prev) => ({
                                                                            ...prev,
                                                                            [session.id]:
                                                                                Date.now(),
                                                                        })
                                                                    )
                                                                    closeDrawer()
                                                                }
                                                            }}
                                                            onLongPress={() => {
                                                                setDeletingSessionId(session.id)
                                                            }}>
                                                            <View style={styles.sessionNodeBody}>
                                                                <View
                                                                    style={{
                                                                        flexDirection: 'row',
                                                                        alignItems: 'center',
                                                                        columnGap: 8,
                                                                    }}>
                                                                    <Animated.View
                                                                        style={[
                                                                            styles.statusDot,
                                                                            isSessionOnline
                                                                                ? styles.statusDotOnline
                                                                                : styles.statusDotOffline,
                                                                            {
                                                                                opacity:
                                                                                    isSessionOnline
                                                                                        ? isSessionBusy
                                                                                            ? blinkAnim
                                                                                            : 1.0
                                                                                        : 1.0,
                                                                            },
                                                                        ]}
                                                                    />
                                                                    <Feather
                                                                        name="message-square"
                                                                        size={11}
                                                                        color={
                                                                            isActiveSession
                                                                                ? '#38bdf8'
                                                                                : '#4b5563'
                                                                        }
                                                                    />
                                                                    <View
                                                                        style={{
                                                                            flexDirection: 'column',
                                                                            flex: 1,
                                                                        }}>
                                                                        <Text
                                                                            style={[
                                                                                styles.sessionNodeText,
                                                                                isActiveSession &&
                                                                                    styles.sessionNodeTextActive,
                                                                            ]}
                                                                            numberOfLines={1}>
                                                                            {display.primary}
                                                                        </Text>
                                                                        {display.secondary ? (
                                                                            <Text
                                                                                style={
                                                                                    styles.sessionNodeSubtext
                                                                                }
                                                                                numberOfLines={1}>
                                                                                {display.secondary}
                                                                            </Text>
                                                                        ) : null}
                                                                    </View>
                                                                </View>
                                                            </View>
                                                            {deletingSessionId === session.id ? (
                                                                <View
                                                                    style={{
                                                                        flexDirection: 'row',
                                                                        alignItems: 'center',
                                                                        columnGap: 6,
                                                                    }}>
                                                                    <Pressable
                                                                        style={({ pressed }) => [
                                                                            styles.deleteConfirmBtn,
                                                                            pressed && {
                                                                                opacity: 0.7,
                                                                            },
                                                                        ]}
                                                                        onPress={async (e) => {
                                                                            e.stopPropagation()
                                                                            await handleDeleteSession(
                                                                                session.id
                                                                            )
                                                                        }}>
                                                                        <Text
                                                                            style={
                                                                                styles.deleteConfirmText
                                                                            }>
                                                                            移除
                                                                        </Text>
                                                                    </Pressable>
                                                                    <Pressable
                                                                        style={({ pressed }) => [
                                                                            styles.deleteCancelBtn,
                                                                            pressed && {
                                                                                opacity: 0.7,
                                                                            },
                                                                        ]}
                                                                        onPress={(e) => {
                                                                            e.stopPropagation()
                                                                            setDeletingSessionId(
                                                                                null
                                                                            )
                                                                        }}>
                                                                        <Feather
                                                                            name="x"
                                                                            size={14}
                                                                            color="#94a3b8"
                                                                        />
                                                                    </Pressable>
                                                                </View>
                                                            ) : (
                                                                isActiveSession && (
                                                                    <Feather
                                                                        name="check"
                                                                        size={12}
                                                                        color="#38bdf8"
                                                                    />
                                                                )
                                                            )}
                                                        </Pressable>
                                                    )
                                                })}
                                        </View>
                                    )
                                })
                            })()
                        )}
                    </ScrollView>

                    <View style={styles.drawerFooter}>
                        <Text style={styles.drawerFooterLabel}>当前大语言模型</Text>
                        <Text style={styles.drawerFooterVal} numberOfLines={1}>
                            {modelName}
                        </Text>
                        <View style={styles.drawerFooterDivider} />
                        <View style={styles.drawerVersionRow}>
                            <View style={styles.drawerVersionInfo}>
                                <Text style={styles.drawerFooterLabel}>移动端版本</Text>
                                <Text style={styles.drawerFooterVal} numberOfLines={1}>
                                    v{nativeVersion} ({nativeBuildVersion})
                                </Text>
                            </View>
                            <Pressable
                                disabled={mobileUpdateButtonDisabled}
                                onPress={() => {
                                    if (mobileUpdateStatus === 'available') {
                                        void handleInstallMobileUpdate()
                                    } else {
                                        void handleCheckMobileUpdate()
                                    }
                                }}
                                style={[
                                    styles.drawerUpdateButton,
                                    mobileUpdateButtonDisabled && styles.drawerUpdateButtonDisabled,
                                ]}>
                                {mobileUpdateButtonDisabled ? (
                                    <ActivityIndicator size="small" color="#93c5fd" />
                                ) : (
                                    <Feather
                                        name={
                                            mobileUpdateStatus === 'available'
                                                ? 'download'
                                                : 'refresh-cw'
                                        }
                                        size={13}
                                        color="#bfdbfe"
                                    />
                                )}
                                <Text style={styles.drawerUpdateButtonText}>
                                    {mobileUpdateButtonText}
                                </Text>
                            </Pressable>
                        </View>
                        <Text
                            style={[
                                styles.drawerUpdateStatusText,
                                mobileUpdateStatus === 'error' && styles.drawerUpdateErrorText,
                                mobileUpdateStatus === 'available' &&
                                    styles.drawerUpdateAvailableText,
                            ]}
                            numberOfLines={2}>
                            {mobileUpdateStateText}
                        </Text>
                    </View>
                </Animated.View>
            </KeyboardAvoidingView>

            <FullTerminalModal
                visible={terminalModalVisible}
                title={terminalModalTitle}
                input={terminalModalInput}
                content={terminalModalContent}
                isError={terminalModalIsError}
                onClose={() => setTerminalModalVisible(false)}
            />

            <Modal
                visible={quickSelectRole !== null}
                animationType="fade"
                transparent={true}
                onRequestClose={() => setQuickSelectRole(null)}>
                <Pressable style={styles.modalBg} onPress={() => setQuickSelectRole(null)}>
                    <Pressable
                        style={styles.quickModelModalContent}
                        onPress={(e) => e.stopPropagation()}>
                        <View style={styles.quickModelModalHeader}>
                            <Text style={styles.quickModelModalTitle}>
                                切换 {quickSelectRole === 'brain' ? 'Brain' : 'Worker'} 模型
                            </Text>
                            <Pressable
                                onPress={() => setQuickSelectRole(null)}
                                style={styles.modalHeaderBtn}>
                                <Feather name="x" size={16} color="#f87171" />
                            </Pressable>
                        </View>
                        <View style={{ padding: 12 }}>
                            {activeSession?.available_models &&
                            activeSession.available_models.length > 0 ? (
                                <ScrollView style={{ maxHeight: 300 }} nestedScrollEnabled={true}>
                                    {activeSession.available_models.map((m) => {
                                        const currentProvider =
                                            activeSession.models?.[quickSelectRole || 'brain']
                                                ?.provider ||
                                            (quickSelectRole === 'brain'
                                                ? activeSession.provider
                                                : '') ||
                                            ''
                                        const currentModel =
                                            activeSession.models?.[quickSelectRole || 'brain']
                                                ?.model ||
                                            (quickSelectRole === 'brain'
                                                ? activeSession.model
                                                : '') ||
                                            ''
                                        const isSelected =
                                            currentProvider === m.provider &&
                                            currentModel === m.model
                                        return (
                                            <Pressable
                                                key={`${m.provider}/${m.model}`}
                                                onPress={() => {
                                                    if (quickSelectRole) {
                                                        handleQuickSwitchModel(
                                                            quickSelectRole,
                                                            m.provider,
                                                            m.model
                                                        )
                                                    }
                                                }}
                                                style={[
                                                    styles.modelOptionRow,
                                                    isSelected && styles.modelOptionRowActive,
                                                ]}>
                                                <View style={styles.modelOptionInfo}>
                                                    <Text
                                                        style={[
                                                            styles.modelOptionProvider,
                                                            isSelected &&
                                                                styles.modelOptionTextActive,
                                                        ]}>
                                                        {m.provider}
                                                    </Text>
                                                    <Text
                                                        style={[
                                                            styles.modelOptionName,
                                                            isSelected &&
                                                                styles.modelOptionTextActive,
                                                        ]}>
                                                        {m.model}
                                                    </Text>
                                                </View>
                                                {isSelected && (
                                                    <Feather
                                                        name="check"
                                                        size={14}
                                                        color="#38bdf8"
                                                    />
                                                )}
                                            </Pressable>
                                        )
                                    })}
                                </ScrollView>
                            ) : (
                                <View style={{ paddingVertical: 20, alignItems: 'center' }}>
                                    <Text style={{ color: '#64748b', fontSize: 12 }}>
                                        无可用模型
                                    </Text>
                                </View>
                            )}
                        </View>
                    </Pressable>
                </Pressable>
            </Modal>
        </View>
    )
}

export default CrushMobile

const styles = StyleSheet.create({
    safeArea: {
        flex: 1,
        backgroundColor: '#07090e',
    },
    root: {
        flex: 1,
    },
    header: {
        paddingHorizontal: 16,
        paddingTop: 10,
        paddingBottom: 12,
        rowGap: 10,
        borderBottomWidth: 1,
        borderBottomColor: '#161f30',
        backgroundColor: '#0a0d14',
    },
    headerTop: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
    },
    titleContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
    },
    title: {
        color: '#f8fafc',
        fontSize: 20,
        fontWeight: '900',
        letterSpacing: -0.5,
    },
    statusDot: {
        width: 7,
        height: 7,
        borderRadius: 999,
        backgroundColor: '#94a3b8',
    },
    statusDotOnline: {
        backgroundColor: '#10b981',
    },
    statusDotBusy: {
        backgroundColor: '#f59e0b',
    },
    statusDotOffline: {
        backgroundColor: '#ef4444',
    },
    statusText: {
        color: '#64748b',
        fontSize: 11,
        fontWeight: '700',
    },
    settingsButton: {
        width: 32,
        height: 32,
        borderRadius: 8,
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: '#111827',
        borderWidth: 1,
        borderColor: '#1f2937',
    },
    settingsButtonActive: {
        borderColor: '#38bdf8',
        backgroundColor: '#0c4a6e',
    },
    serverPanel: {
        padding: 10,
        backgroundColor: '#0f172a',
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#1e293b',
        marginTop: 4,
    },
    serverRow: {
        flexDirection: 'row',
        columnGap: 8,
    },
    serverInput: {
        flex: 1,
        height: 38,
        borderRadius: 6,
        borderWidth: 1,
        borderColor: '#334155',
        paddingHorizontal: 10,
        color: '#f8fafc',
        backgroundColor: '#020617',
        fontSize: 13,
    },
    connectButton: {
        width: 60,
        height: 38,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 6,
        backgroundColor: '#6366f1',
    },
    buttonText: {
        color: '#ffffff',
        fontWeight: '700',
        fontSize: 12,
    },
    modelPicker: {
        marginTop: 12,
        paddingTop: 12,
        rowGap: 8,
        borderTopWidth: 1,
        borderTopColor: '#1f2937',
    },
    modelPickerLabel: {
        color: '#94a3b8',
        fontSize: 12,
        fontWeight: '600',
    },
    modelRoleRow: {
        flexDirection: 'row',
        columnGap: 6,
    },
    modelRoleChip: {
        flex: 1,
        paddingVertical: 6,
        borderRadius: 6,
        backgroundColor: '#0f172a',
        borderWidth: 1,
        borderColor: '#1f2937',
        alignItems: 'center',
    },
    modelRoleChipActive: {
        backgroundColor: '#1e3a8a',
        borderColor: '#38bdf8',
    },
    modelRoleChipText: {
        color: '#94a3b8',
        fontSize: 12,
        fontWeight: '600',
    },
    modelRoleChipTextActive: {
        color: '#e0f2fe',
    },
    modelSaveDisabled: {
        opacity: 0.5,
    },
    cwdGroup: {
        marginBottom: 2,
    },
    cwdGroupHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        paddingHorizontal: 16,
        paddingVertical: 8,
        borderRadius: 6,
    },
    cwdGroupHeaderActive: {
        backgroundColor: '#0b1c2e',
    },
    cwdGroupTitle: {
        flex: 1,
        color: '#94a3b8',
        fontSize: 12,
        fontWeight: '600',
    },
    cwdGroupTitleActive: {
        color: '#38bdf8',
    },
    cwdGroupCount: {
        color: '#4b5563',
        fontSize: 10,
        fontVariant: ['tabular-nums'],
    },
    sessionNodeIndented: {
        marginLeft: 12,
    },
    compactMetaRow: {
        flexDirection: 'row',
        columnGap: 8,
        marginTop: 2,
    },
    metaBadge: {
        flex: 1,
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        height: 36,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#1e293b',
        paddingHorizontal: 12,
        backgroundColor: '#0b0f19',
    },
    metaBadgeText: {
        color: '#94a3b8',
        fontSize: 10.5,
        fontWeight: '600',
    },
    errorText: {
        color: '#f87171',
        fontSize: 11,
        marginTop: 6,
    },
    content: {
        flex: 1,
    },
    sessionRail: {
        height: 48,
        paddingVertical: 8,
        paddingHorizontal: 12,
        backgroundColor: '#080b11',
    },
    sessionChip: {
        height: 30,
        maxWidth: 160,
        paddingHorizontal: 12,
        justifyContent: 'center',
        borderRadius: 15,
        marginRight: 6,
        borderWidth: 1,
        borderColor: '#1e293b',
        backgroundColor: '#0f172a',
    },
    sessionChipActive: {
        borderColor: '#38bdf8',
        backgroundColor: '#0c4a6e',
    },
    sessionChipText: {
        color: '#64748b',
        fontSize: 11,
        fontWeight: '700',
    },
    sessionChipTextActive: {
        color: '#f0f9ff',
    },
    newSessionChip: {
        height: 30,
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 4,
        paddingHorizontal: 12,
        justifyContent: 'center',
        borderRadius: 15,
        marginRight: 8,
        borderWidth: 1,
        borderColor: '#065f46',
        backgroundColor: '#064e3b',
    },
    newSessionChipText: {
        color: '#a7f3d0',
        fontSize: 11,
        fontWeight: '800',
    },
    activitiesSection: {
        paddingVertical: 6,
        backgroundColor: '#080b11',
    },
    activityPanel: {
        maxHeight: 74,
        paddingHorizontal: 12,
    },
    activityCard: {
        width: 150,
        height: 60,
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#1e293b',
        backgroundColor: '#0f172a',
        paddingVertical: 6,
        paddingHorizontal: 8,
        marginRight: 6,
    },
    activity_running: {
        borderColor: '#b45309',
        backgroundColor: '#78350f22',
    },
    activity_done: {
        borderColor: '#047857',
        backgroundColor: '#064e3b22',
    },
    activity_failed: {
        borderColor: '#b91c1c',
        backgroundColor: '#7f1d1d22',
    },
    activityCardHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 4,
    },
    activityRole: {
        color: '#38bdf8',
        fontSize: 9,
        fontWeight: '800',
        textTransform: 'uppercase',
    },
    activityTitle: {
        color: '#f1f5f9',
        fontSize: 11,
        fontWeight: '700',
        marginTop: 2,
    },
    activityText: {
        color: '#64748b',
        fontSize: 9,
        marginTop: 1,
    },
    messages: {
        flex: 1,
        backgroundColor: '#07090e',
    },
    messagesContent: {
        paddingHorizontal: 16,
        paddingTop: 16,
        paddingBottom: 24,
        rowGap: 18,
    },
    loadOlderText: {
        textAlign: 'center',
        color: '#6b7280',
        fontSize: 12,
        paddingVertical: 8,
    },
    messageRow: {
        flexDirection: 'row',
        width: '100%',
    },
    userRow: {
        justifyContent: 'flex-end',
    },
    assistantRow: {
        justifyContent: 'flex-start',
    },
    messageBubble: {
        // Core padding is handled inside specific bubbles
    },
    userBubble: {
        maxWidth: '80%',
        alignSelf: 'flex-end',
        backgroundColor: '#6366f1', // Premium indigo for user
        paddingHorizontal: 16,
        paddingVertical: 11,
        borderTopLeftRadius: 20,
        borderTopRightRadius: 20,
        borderBottomLeftRadius: 20,
        borderBottomRightRadius: 4, // Telegram-like tail corner for user (right)
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 1.5 },
        shadowOpacity: 0.12,
        shadowRadius: 3,
        elevation: 2,
    },
    assistantBubble: {
        maxWidth: '80%',
        alignSelf: 'flex-start',
        backgroundColor: '#1e293b', // Premium deep slate for agent
        borderWidth: 1,
        borderColor: '#242f41',
        paddingHorizontal: 16,
        paddingVertical: 11,
        borderTopLeftRadius: 20,
        borderTopRightRadius: 20,
        borderBottomLeftRadius: 4, // Telegram-like tail corner for assistant (left)
        borderBottomRightRadius: 20,
        rowGap: 6,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 1.5 },
        shadowOpacity: 0.12,
        shadowRadius: 3,
        elevation: 2,
    },
    typingIndicatorContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
        paddingVertical: 4,
    },
    typingText: {
        color: '#94a3b8',
        fontSize: 13.5,
    },
    canceledContainer: {
        paddingVertical: 2,
    },
    canceledText: {
        color: '#cbd5e1',
        fontSize: 13.5,
        fontStyle: 'italic',
        fontFamily: 'FiraCode-Regular',
    },
    interruptDivider: {
        width: '100%',
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
        paddingVertical: 8,
    },
    interruptDividerLine: {
        flex: 1,
        height: 1,
        backgroundColor: '#64748b',
        opacity: 0.55,
    },
    interruptDividerText: {
        color: '#cbd5e1',
        fontSize: 12,
        fontFamily: 'FiraCode-Regular',
    },
    messageHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
    },
    terminalCardContainer: {
        borderWidth: 1,
        borderColor: '#1e293b',
        backgroundColor: '#0b1329ee',
        borderRadius: 8,
        marginVertical: 6,
        overflow: 'hidden',
        width: '100%',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.25,
        shadowRadius: 5,
        elevation: 6,
    },
    terminalCardError: {
        borderColor: '#7f1d1d',
        backgroundColor: '#180f15ee',
    },
    terminalHeader: {
        height: 32,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        backgroundColor: '#0f172a',
        paddingHorizontal: 10,
        borderBottomWidth: 1,
        borderBottomColor: '#1e293b',
    },
    terminalHeaderLeft: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 5,
    },
    macDot: {
        width: 8,
        height: 8,
        borderRadius: 4,
    },
    terminalHeaderTitle: {
        color: '#64748b',
        fontSize: 10.5,
        fontWeight: '700',
        fontFamily: 'FiraCode-Regular',
        textTransform: 'uppercase',
    },
    terminalHeaderRight: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
    },
    terminalHeaderButton: {
        padding: 4,
    },
    terminalCommandRow: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: '#020617',
        paddingHorizontal: 12,
        paddingTop: 8,
        paddingBottom: 4,
        columnGap: 6,
    },
    terminalPrompt: {
        color: '#10b981',
        fontSize: 11,
        fontWeight: 'bold',
        fontFamily: 'FiraCode-Regular',
    },
    terminalCommandText: {
        color: '#f1f5f9',
        fontSize: 11.5,
        fontFamily: 'FiraCode-Regular',
        flex: 1,
    },
    terminalOutputContainer: {
        backgroundColor: '#020617',
        paddingHorizontal: 12,
        paddingBottom: 8,
        position: 'relative',
    },
    terminalOutputScroll: {
        maxHeight: 180,
    },
    terminalOutputText: {
        color: '#cbd5e1',
        fontSize: 11,
        lineHeight: 15,
        fontFamily: 'FiraCode-Regular',
    },
    terminalFoldOverlay: {
        borderTopWidth: 1,
        borderTopColor: '#1e293b',
        paddingTop: 6,
        marginTop: 6,
        alignItems: 'center',
    },
    terminalExpandButton: {
        paddingVertical: 2,
    },
    terminalExpandText: {
        color: '#38bdf8',
        fontSize: 10,
        fontWeight: '700',
    },
    modalBg: {
        flex: 1,
        backgroundColor: 'rgba(2, 6, 23, 0.85)',
        justifyContent: 'center',
        alignItems: 'center',
    },
    modalContent: {
        width: '94%',
        height: '84%',
        backgroundColor: '#020617',
        borderRadius: 12,
        borderWidth: 1,
        borderColor: '#1e293b',
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 10 },
        shadowOpacity: 0.5,
        shadowRadius: 10,
        elevation: 12,
    },
    modalHeader: {
        height: 48,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        backgroundColor: '#0f172a',
        paddingHorizontal: 16,
        borderBottomWidth: 1,
        borderBottomColor: '#1e293b',
    },
    modalHeaderLeft: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    modalTitle: {
        color: '#f1f5f9',
        fontSize: 13,
        fontWeight: 'bold',
        fontFamily: 'FiraCode-Regular',
    },
    modalHeaderRight: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 14,
    },
    modalHeaderBtn: {
        padding: 6,
        borderRadius: 4,
        backgroundColor: '#1e293b',
    },
    modalFontText: {
        color: '#94a3b8',
        fontSize: 11,
        fontWeight: '600',
    },
    modalCloseBtn: {
        backgroundColor: '#7f1d1d33',
    },
    modalLogScroll: {
        flex: 1,
        backgroundColor: '#020617',
    },
    modalLogScrollContent: {
        padding: 16,
    },
    modalLogText: {
        color: '#cbd5e1',
        lineHeight: 16,
        fontFamily: 'FiraCode-Regular',
    },
    toolCallTitle: {
        color: '#38bdf8',
        fontSize: 12,
        fontWeight: '700',
    },
    messageRoleText: {
        fontSize: 10.5,
        fontWeight: '800',
        textTransform: 'uppercase',
        letterSpacing: 0.5,
    },
    userRoleText: {
        color: '#93c5fd',
    },
    assistantRoleText: {
        color: '#c084fc',
    },
    messageText: {
        color: '#f8fafc',
        fontSize: 14.2,
        lineHeight: 21.5,
    },
    compoundTextContainer: {
        maxWidth: '85%',
        alignSelf: 'flex-start',
        backgroundColor: '#131b2e88',
        borderWidth: 1,
        borderColor: '#242f41',
        paddingHorizontal: 16,
        paddingVertical: 12,
        borderRadius: 16,
        borderBottomLeftRadius: 4,
        marginTop: 6,
        marginBottom: 10,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 1.5 },
        shadowOpacity: 0.1,
        shadowRadius: 3,
        elevation: 1,
    },
    compoundTypingContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
        backgroundColor: '#131b2e88',
        borderWidth: 1,
        borderColor: '#1e293b66',
        borderRadius: 12,
        paddingHorizontal: 16,
        paddingVertical: 12,
        marginVertical: 4,
        width: '100%',
        alignSelf: 'stretch',
    },
    compoundTypingText: {
        color: '#a78bfa',
        fontSize: 13,
    },
    thinkingBubble: {
        width: '100%',
        alignSelf: 'stretch',
        backgroundColor: '#131127aa', // Premium translucent deep purple
        borderWidth: 1,
        borderColor: '#3b2f63',
        borderLeftWidth: 3,
        borderLeftColor: '#c084fc',
        borderRadius: 12,
        marginVertical: 4,
        shadowColor: '#c084fc',
        shadowOffset: { width: 0, height: 1.5 },
        shadowOpacity: 0.08,
        shadowRadius: 3,
        elevation: 1,
        overflow: 'hidden',
    },
    thinkingHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingVertical: 8,
        paddingHorizontal: 12,
        columnGap: 6,
    },
    thinkingHeaderText: {
        color: '#c084fc',
        fontSize: 12,
        fontWeight: '600',
        letterSpacing: 0.5,
    },
    thinkingBody: {
        paddingHorizontal: 12,
        paddingBottom: 10,
        borderTopWidth: StyleSheet.hairlineWidth,
        borderTopColor: '#3b2f6333',
    },
    thinkingText: {
        color: '#d8b4fe', // Soft violet for thinking process content
        fontSize: 11.5,
        lineHeight: 16.5,
        fontStyle: 'italic',
    },
    detailsBubble: {
        width: '100%',
        alignSelf: 'stretch',
        backgroundColor: '#1e293b22', // Slate translucent background
        borderWidth: 1,
        borderColor: '#33415555',
        borderLeftWidth: 3,
        borderLeftColor: '#8b5cf6', // Purple accent
        borderRadius: 8,
        marginVertical: 6,
        overflow: 'hidden',
    },
    detailsHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingVertical: 10,
        paddingHorizontal: 12,
        columnGap: 8,
    },
    detailsHeaderText: {
        color: '#e2e8f0',
        fontSize: 13,
        fontWeight: '600',
    },
    detailsBody: {
        paddingHorizontal: 12,
        paddingBottom: 12,
        borderTopWidth: 1,
        borderTopColor: '#33415533',
        backgroundColor: '#0f172a22',
    },
    toolCallContainer: {
        borderLeftWidth: 3,
        borderLeftColor: '#38bdf8',
        backgroundColor: '#090d16',
        borderRadius: 4,
        paddingVertical: 6,
        paddingHorizontal: 10,
        marginVertical: 4,
    },
    toolCallHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        marginBottom: 4,
    },
    toolResultContainer: {
        borderLeftWidth: 3,
        borderLeftColor: '#10b981',
        backgroundColor: '#050c0a',
        borderRadius: 4,
        paddingVertical: 6,
        paddingHorizontal: 10,
        marginVertical: 4,
    },
    toolResultError: {
        borderLeftColor: '#ef4444',
        backgroundColor: '#110707',
    },
    toolResultHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        marginBottom: 4,
    },
    toolResultTitle: {
        fontSize: 12,
        fontWeight: '700',
    },
    toolResultTitleSuccess: {
        color: '#34d399',
    },
    toolResultTitleError: {
        color: '#f87171',
    },
    codeBlock: {
        backgroundColor: '#020617',
        borderRadius: 4,
        padding: 6,
        marginTop: 2,
    },
    codeText: {
        color: '#e2e8f0',
        fontSize: 11,
        fontFamily: 'FiraCode-Regular',
    },
    expandCodeButton: {
        alignItems: 'center',
        paddingVertical: 4,
        borderTopWidth: 1,
        borderTopColor: '#1e293b',
        marginTop: 4,
    },
    expandCodeText: {
        color: '#94a3b8',
        fontSize: 10,
        fontWeight: '700',
    },
    emptyState: {
        alignItems: 'center',
        paddingVertical: 80,
        paddingHorizontal: 32,
        rowGap: 10,
    },
    emptyTitle: {
        color: '#f8fafc',
        fontSize: 15,
        fontWeight: '800',
    },
    emptyText: {
        color: '#64748b',
        fontSize: 12,
        textAlign: 'center',
        lineHeight: 17,
    },
    permissionPanel: {
        paddingHorizontal: 12,
        paddingBottom: 8,
        rowGap: 6,
        backgroundColor: '#07090e',
    },
    permissionCard: {
        borderRadius: 8,
        borderWidth: 1,
        borderColor: '#d97706',
        backgroundColor: '#451a0333',
        padding: 10,
        rowGap: 6,
    },
    permissionHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
    },
    permissionTitle: {
        color: '#fbbf24',
        fontSize: 12,
        fontWeight: '800',
    },
    permissionText: {
        color: '#fcd34d',
        fontSize: 11,
    },
    permissionActions: {
        flexDirection: 'row',
        columnGap: 8,
    },
    allowButton: {
        flex: 1,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 6,
        paddingVertical: 8,
        backgroundColor: '#059669',
    },
    denyButton: {
        width: 64,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 6,
        paddingVertical: 8,
        backgroundColor: '#dc2626',
    },
    inputBar: {
        flexDirection: 'row',
        alignItems: 'flex-end',
        columnGap: 8,
        paddingHorizontal: 16,
        paddingTop: 8,
        paddingBottom: 16,
        borderTopWidth: 1,
        borderTopColor: '#161f30',
        backgroundColor: '#0a0d14',
    },
    promptInput: {
        flex: 1,
        minHeight: 38,
        maxHeight: 100,
        borderRadius: 19,
        borderWidth: 1,
        borderColor: '#242f41',
        color: '#f8fafc',
        backgroundColor: '#0f131a',
        paddingHorizontal: 16,
        paddingVertical: 8,
        fontSize: 14,
    },
    sendButton: {
        width: 38,
        height: 38,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 19,
        backgroundColor: '#6366f1',
    },
    stopButton: {
        width: 38,
        height: 38,
        alignItems: 'center',
        justifyContent: 'center',
        borderRadius: 19,
        backgroundColor: '#ef4444',
    },
    disabledButton: {
        opacity: 0.45,
    },
    pressed: {
        opacity: 0.7,
    },
    markdownContainer: {
        width: '100%',
    },
    markdownTextBlock: {
        width: '100%',
    },
    markdownParagraph: {
        color: '#f8fafc',
        fontSize: 14.2,
        lineHeight: 21.5,
        marginBottom: 8,
    },
    markdownH1: {
        color: '#f8fafc',
        fontSize: 20,
        lineHeight: 27,
        fontWeight: '800',
        marginTop: 14,
        marginBottom: 8,
    },
    markdownH2: {
        color: '#f8fafc',
        fontSize: 17.5,
        lineHeight: 24,
        fontWeight: '700',
        marginTop: 12,
        marginBottom: 6,
    },
    markdownH3: {
        color: '#f8fafc',
        fontSize: 15.5,
        lineHeight: 22,
        fontWeight: '700',
        marginTop: 10,
        marginBottom: 6,
    },
    markdownH4: {
        color: '#f8fafc',
        fontSize: 14.5,
        lineHeight: 20,
        fontWeight: '600',
        marginTop: 8,
        marginBottom: 4,
    },
    markdownListItemRow: {
        flexDirection: 'row',
        alignItems: 'flex-start',
        marginBottom: 7,
    },
    markdownListBullet: {
        color: '#a78bfa',
        fontSize: 14,
        marginRight: 6,
        lineHeight: 21.5,
    },
    markdownListNum: {
        color: '#a78bfa',
        fontSize: 13,
        fontWeight: '700',
        marginRight: 6,
        lineHeight: 21.5,
    },
    markdownListItemText: {
        flex: 1,
        color: '#e2e8f0',
        fontSize: 14.2,
        lineHeight: 21.5,
    },
    markdownInlineCode: {
        fontFamily: 'FiraCode-Regular',
        backgroundColor: '#131224',
        color: '#a78bfa',
        fontSize: 11.5,
        fontWeight: '600',
        paddingHorizontal: 6,
        paddingVertical: 1.5,
        borderRadius: 4,
        borderWidth: 1,
        borderColor: '#2e264d',
    },
    markdownCodeBlockContainer: {
        backgroundColor: '#020617',
        borderWidth: 1,
        borderColor: '#1e293b',
        borderRadius: 6,
        padding: 10,
        marginVertical: 8,
        width: '100%',
    },
    markdownCodeBlockLang: {
        color: '#64748b',
        fontSize: 9,
        fontWeight: 'bold',
        textTransform: 'uppercase',
        alignSelf: 'flex-end',
        marginBottom: 4,
    },
    markdownCodeBlockText: {
        color: '#e2e8f0',
        fontSize: 11,
        lineHeight: 16,
        fontFamily: 'FiraCode-Regular',
    },
    markdownCodeBlockHeader: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        borderBottomWidth: 1,
        borderBottomColor: '#1e293b',
        paddingBottom: 4,
        marginBottom: 6,
    },
    copyButton: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 4,
        backgroundColor: '#1e293b',
        paddingHorizontal: 8,
        paddingVertical: 3,
        borderRadius: 4,
    },
    copyButtonText: {
        color: '#94a3b8',
        fontSize: 9,
        fontWeight: 'bold',
    },
    markdownBlockquote: {
        borderLeftWidth: 3,
        borderLeftColor: '#8b5cf6',
        backgroundColor: '#16132844',
        paddingHorizontal: 10,
        paddingVertical: 6,
        borderRadius: 4,
        marginVertical: 6,
    },
    markdownBlockquoteText: {
        color: '#c084fc',
        fontStyle: 'italic',
        fontSize: 13,
        lineHeight: 18,
    },
    markdownHR: {
        height: 1,
        backgroundColor: '#1e293b',
        marginVertical: 10,
        width: '100%',
    },
    toolResultHeaderRow: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        marginBottom: 6,
    },

    // Drawer styles
    drawerContainer: {
        position: 'absolute',
        top: 0,
        left: 0,
        bottom: 0,
        width: DRAWER_WIDTH,
        backgroundColor: '#0a0d14',
        borderRightWidth: 1,
        borderRightColor: '#161f30',
        zIndex: 100,
        paddingTop: Platform.OS === 'ios' ? 44 : 20,
    },
    drawerBackdrop: {
        position: 'absolute',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: '#000000',
        zIndex: 99,
    },
    drawerHeader: {
        paddingHorizontal: 20,
        paddingVertical: 18,
        borderBottomWidth: 1,
        borderBottomColor: '#161f30',
    },
    drawerHeaderTitleRow: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 8,
    },
    drawerHeaderTitle: {
        color: '#f8fafc',
        fontSize: 18,
        fontWeight: '900',
        letterSpacing: -0.5,
    },
    drawerHeaderSub: {
        color: '#64748b',
        fontSize: 11,
        marginTop: 4,
    },
    drawerHeaderActiveWorkspace: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        backgroundColor: '#0c1524',
        borderWidth: 1,
        borderColor: '#1e293b',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 4,
        marginTop: 8,
    },
    drawerHeaderActiveWorkspaceText: {
        color: '#38bdf8',
        fontSize: 11,
        fontWeight: '500',
    },
    drawerHeaderActiveSession: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        backgroundColor: '#061c16',
        borderWidth: 1,
        borderColor: '#064e3b',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 4,
        marginTop: 6,
    },
    drawerHeaderActiveSessionText: {
        color: '#10b981',
        fontSize: 11,
        fontWeight: '500',
    },
    drawerScroll: {
        flex: 1,
        paddingVertical: 12,
    },
    drawerSectionTitle: {
        color: '#475569',
        fontSize: 10,
        fontWeight: '800',
        textTransform: 'uppercase',
        letterSpacing: 1.0,
        paddingHorizontal: 20,
        marginBottom: 8,
        marginTop: 12,
    },
    workspaceNode: {
        marginBottom: 12,
    },
    workspaceItem: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        paddingHorizontal: 20,
        paddingVertical: 10,
        backgroundColor: 'transparent',
    },
    workspaceItemActive: {
        backgroundColor: '#0f172a',
    },
    workspacePressed: {
        backgroundColor: '#1e293b',
    },
    workspaceRight: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    workspaceLeft: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 10,
        flex: 1,
    },
    workspaceName: {
        color: '#94a3b8',
        fontSize: 13.5,
        fontWeight: '600',
    },
    workspaceNameActive: {
        color: '#f8fafc',
        fontWeight: '700',
    },
    sessionsContainer: {
        paddingLeft: 34,
        marginTop: 4,
        rowGap: 4,
    },
    sessionNode: {
        flexDirection: 'row',
        alignItems: 'flex-start',
        justifyContent: 'space-between',
        minHeight: 44,
        paddingHorizontal: 12,
        paddingVertical: 8,
        borderRadius: 6,
    },
    sessionNodeActive: {
        backgroundColor: '#1e293b',
        borderLeftWidth: 3,
        borderLeftColor: '#38bdf8',
        paddingLeft: 9,
    },
    sessionNodePressed: {
        backgroundColor: '#0f172a',
    },
    sessionNodeBody: {
        flex: 1,
        minWidth: 0,
        rowGap: 2,
    },
    sessionNodeText: {
        color: '#64748b',
        fontSize: 12,
        fontWeight: '600',
        flex: 1,
    },
    sessionNodeTextActive: {
        color: '#f0f9ff',
        fontWeight: '700',
    },
    sessionNodeSubtitle: {
        color: '#475569',
        fontSize: 10.5,
        fontWeight: '500',
        paddingLeft: 19,
    },
    sessionNodeSubtitleActive: {
        color: '#94a3b8',
    },
    drawerNewSessionBtn: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        paddingVertical: 8,
        paddingHorizontal: 12,
        alignSelf: 'flex-start',
        borderRadius: 6,
        borderWidth: 1,
        borderColor: '#065f46',
        backgroundColor: '#064e3b',
        marginTop: 6,
        marginLeft: 12,
    },
    drawerNewSessionText: {
        color: '#a7f3d0',
        fontSize: 11,
        fontWeight: '800',
    },
    drawerFooter: {
        padding: 16,
        borderTopWidth: 1,
        borderTopColor: '#161f30',
        backgroundColor: '#080b11',
    },
    drawerFooterLabel: {
        color: '#475569',
        fontSize: 9.5,
        fontWeight: '800',
        textTransform: 'uppercase',
    },
    drawerFooterVal: {
        color: '#94a3b8',
        fontSize: 11,
        marginTop: 2,
        fontWeight: '600',
    },
    drawerFooterDivider: {
        height: 1,
        backgroundColor: '#162033',
        marginVertical: 10,
    },
    drawerVersionRow: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 10,
    },
    drawerVersionInfo: {
        flex: 1,
        minWidth: 0,
    },
    drawerUpdateButton: {
        height: 30,
        minWidth: 72,
        paddingHorizontal: 10,
        borderRadius: 6,
        borderWidth: 1,
        borderColor: '#1d4ed8',
        backgroundColor: '#0f172a',
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        columnGap: 5,
    },
    drawerUpdateButtonDisabled: {
        opacity: 0.68,
    },
    drawerUpdateButtonText: {
        color: '#bfdbfe',
        fontSize: 10.5,
        fontWeight: '800',
    },
    drawerUpdateStatusText: {
        color: '#64748b',
        fontSize: 10,
        lineHeight: 14,
        marginTop: 7,
    },
    drawerUpdateErrorText: {
        color: '#fca5a5',
    },
    drawerUpdateAvailableText: {
        color: '#93c5fd',
    },
    // Segmented tab styles for Terminal Card
    terminalTabsRow: {
        flexDirection: 'row',
        backgroundColor: '#0a0d14',
        paddingHorizontal: 8,
        paddingVertical: 6,
        columnGap: 6,
        borderBottomWidth: 1,
        borderBottomColor: '#161f30',
    },
    terminalTabButton: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 5,
        paddingVertical: 6,
        paddingHorizontal: 12,
        borderRadius: 6,
        backgroundColor: 'transparent',
    },
    terminalTabButtonActive: {
        backgroundColor: '#1e293b',
        borderWidth: 1,
        borderColor: '#242f41',
    },
    terminalTabText: {
        color: '#64748b',
        fontSize: 11,
        fontWeight: '600',
    },
    terminalTabTextActive: {
        color: '#f8fafc',
    },

    // Directory Tree Viewer Styles
    treeContainer: {
        paddingVertical: 6,
        backgroundColor: '#020617',
    },
    treeRow: {
        flexDirection: 'row',
        alignItems: 'center',
        height: 28,
        paddingVertical: 2,
    },
    treeNodeName: {
        color: '#cbd5e1',
        fontSize: 12,
        fontFamily: 'FiraCode-Regular',
        flex: 1,
    },
    treeNodeDirectory: {
        color: '#f1f5f9',
        fontWeight: '600',
    },

    // Search Matches Viewer Styles
    searchMatchesContainer: {
        rowGap: 8,
        paddingVertical: 6,
        backgroundColor: '#020617',
    },
    matchFileCard: {
        borderWidth: 1,
        borderColor: '#1e293b',
        backgroundColor: '#0b0f19',
        borderRadius: 6,
        overflow: 'hidden',
    },
    matchFileHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        height: 32,
        paddingHorizontal: 8,
        backgroundColor: '#0f172a',
    },
    matchFileName: {
        color: '#cbd5e1',
        fontSize: 11.5,
        fontWeight: '700',
        fontFamily: 'FiraCode-Regular',
    },
    matchFilePath: {
        color: '#64748b',
        fontSize: 9.5,
        fontFamily: 'FiraCode-Regular',
        marginLeft: 4,
        flex: 1,
    },
    matchCountBadge: {
        backgroundColor: '#1e293b',
        paddingHorizontal: 6,
        paddingVertical: 2,
        borderRadius: 10,
        marginLeft: 6,
    },
    matchCountText: {
        color: '#94a3b8',
        fontSize: 9,
        fontWeight: '700',
    },
    matchLinesList: {
        borderTopWidth: 1,
        borderTopColor: '#1e293b',
        paddingVertical: 4,
    },
    matchLineRow: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingVertical: 3,
        paddingHorizontal: 8,
    },
    matchLineNumberCol: {
        width: 32,
        alignItems: 'flex-end',
        marginRight: 8,
    },
    matchLineNumber: {
        color: '#475569',
        fontSize: 10.5,
        fontFamily: 'FiraCode-Regular',
    },
    matchLineContentCol: {
        flex: 1,
    },
    matchLineContentText: {
        color: '#94a3b8',
        fontSize: 11,
        fontFamily: 'FiraCode-Regular',
    },

    // Diff Viewer Styles
    diffContainer: {
        paddingVertical: 4,
        backgroundColor: '#020617',
    },
    diffRow: {
        flexDirection: 'row',
        alignItems: 'flex-start',
        paddingVertical: 2,
        paddingHorizontal: 8,
    },
    diffSign: {
        width: 14,
        fontSize: 11,
        fontFamily: 'FiraCode-Regular',
        fontWeight: 'bold',
    },
    diffLineContent: {
        flex: 1,
    },
    diffLineText: {
        fontSize: 11,
        fontFamily: 'FiraCode-Regular',
    },

    // File Content Viewer Styles
    fileViewerContainer: {
        borderWidth: 1,
        borderColor: '#1e293b',
        backgroundColor: '#020617',
        borderRadius: 6,
        overflow: 'hidden',
    },
    fileViewerToolbar: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        height: 28,
        backgroundColor: '#0f172a',
        paddingHorizontal: 10,
        borderBottomWidth: 1,
        borderBottomColor: '#1e293b',
    },
    fileViewerLangBadge: {
        color: '#64748b',
        fontSize: 9.5,
        fontWeight: '700',
        textTransform: 'uppercase',
    },
    wrapToggleBtn: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 4,
        paddingHorizontal: 8,
        paddingVertical: 3,
        borderRadius: 4,
        backgroundColor: '#1e293b',
    },
    wrapToggleBtnActive: {
        backgroundColor: '#0369a1',
    },
    wrapToggleText: {
        color: '#94a3b8',
        fontSize: 9.5,
        fontWeight: '600',
    },
    wrapToggleTextActive: {
        color: '#ffffff',
    },
    fileViewerScroll: {
        flex: 1,
    },
    fileViewerCodeBlock: {
        paddingVertical: 6,
    },
    codeLineRow: {
        flexDirection: 'row',
        alignItems: 'flex-start',
        paddingHorizontal: 8,
        paddingVertical: 1,
    },
    codeLineNumber: {
        width: 28,
        color: '#475569',
        fontSize: 10,
        fontFamily: 'FiraCode-Regular',
        textAlign: 'right',
        marginRight: 8,
    },
    codeLineText: {
        color: '#cbd5e1',
        fontSize: 11,
        fontFamily: 'FiraCode-Regular',
    },

    // Bubble breakouts and container structures
    messageRowContainer: {
        flexDirection: 'column',
        width: '100%',
        rowGap: 4,
    },
    userRowContainer: {
        alignItems: 'flex-end',
    },
    assistantRowContainer: {
        alignItems: 'flex-start',
    },
    terminalSessionCardWrapper: {
        width: '100%',
        paddingHorizontal: 0,
        marginTop: 4,
        marginBottom: 0,
        alignSelf: 'stretch',
    },
    userMessageHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        alignSelf: 'flex-end',
        marginRight: 16,
        marginBottom: 2,
    },
    assistantMessageHeader: {
        flexDirection: 'row',
        alignItems: 'center',
        columnGap: 6,
        alignSelf: 'flex-start',
        marginLeft: 16,
        marginBottom: 2,
    },
    availableModelsList: {
        borderWidth: 1,
        borderColor: '#1f2937',
        borderRadius: 6,
        backgroundColor: '#020617',
        maxHeight: 120,
    },
    modelsScrollView: {
        padding: 4,
    },
    modelOptionRow: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        paddingVertical: 6,
        paddingHorizontal: 8,
        borderRadius: 4,
        marginBottom: 2,
    },
    modelOptionRowActive: {
        backgroundColor: '#1e293b',
    },
    modelOptionInfo: {
        flexDirection: 'row',
        columnGap: 8,
        alignItems: 'center',
        flex: 1,
    },
    modelOptionProvider: {
        color: '#94a3b8',
        fontSize: 10,
        fontWeight: 'bold',
        textTransform: 'uppercase',
        backgroundColor: '#0f172a',
        paddingHorizontal: 4,
        paddingVertical: 1,
        borderRadius: 3,
        borderWidth: 1,
        borderColor: '#1f2937',
    },
    modelOptionName: {
        color: '#e2e8f0',
        fontSize: 12,
        flex: 1,
    },
    modelOptionTextActive: {
        color: '#38bdf8',
    },
    deleteConfirmBtn: {
        backgroundColor: '#ef444422',
        borderColor: '#f87171',
        borderWidth: 1,
        borderRadius: 4,
        paddingHorizontal: 8,
        paddingVertical: 2,
    },
    deleteConfirmText: {
        color: '#f87171',
        fontSize: 10,
        fontWeight: '700',
    },
    deleteCancelBtn: {
        padding: 4,
    },
    titlePath: {
        color: '#94a3b8',
        fontSize: 10,
        fontWeight: '700',
        backgroundColor: '#1e293b77',
        paddingHorizontal: 6,
        paddingVertical: 2,
        borderRadius: 4,
        marginLeft: 4,
        maxWidth: 110,
    },
    sessionNodeSubtext: {
        color: '#64748b',
        fontSize: 10,
        marginTop: 2,
    },
    quickModelModalContent: {
        width: '90%',
        maxHeight: '60%',
        backgroundColor: '#020617',
        borderRadius: 12,
        borderWidth: 1,
        borderColor: '#1e293b',
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 10 },
        shadowOpacity: 0.5,
        shadowRadius: 10,
        elevation: 12,
    },
    quickModelModalHeader: {
        height: 44,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        backgroundColor: '#0f172a',
        paddingHorizontal: 16,
        borderBottomWidth: 1,
        borderBottomColor: '#1e293b',
    },
    quickModelModalTitle: {
        color: '#f8fafc',
        fontSize: 13,
        fontWeight: 'bold',
    },
})
