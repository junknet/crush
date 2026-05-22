import { fetch as expoFetch } from 'expo/fetch'

export type Workspace = {
    id: string
    path: string
}

export type Session = {
    id: string
    title: string
    updated_at?: number
    created_at?: number
}

export type MessagePart = {
    type: 'text' | 'reasoning' | 'tool_call' | 'tool_result' | string
    data: {
        text?: string
        thinking?: string
        id?: string
        name?: string
        input?: string
        finished?: boolean
        tool_call_id?: string
        content?: string
        is_error?: boolean
        finished_at?: number
    }
}

export type Message = {
    id: string
    role: string
    session_id: string
    parts: MessagePart[]
    created_at?: number
}

export type AgentInfo = {
    is_busy: boolean
    is_ready: boolean
    model?: { id?: string }
    model_cfg?: { model?: string }
}

export type PermissionRequest = {
    id: string
    session_id: string
    tool_call_id: string
    tool_name: string
    description?: string
    path?: string
    params?: unknown
}

export type AgentEvent = {
    type: string
    session_id?: string
    session_title?: string
    sub_agent_tool_call_id?: string
    sub_agent_prompt?: string
    sub_agent_profile?: string
    sub_agent_error?: string
}

export type CrushEnvelope =
    | { type: 'message'; payload: { type: string; payload: Message } }
    | { type: 'session'; payload: { type: string; payload: Session } }
    | { type: 'agent_event'; payload: { type: string; payload: AgentEvent } }
    | { type: 'permission_request'; payload: { type: string; payload: PermissionRequest } }
    | {
          type: 'permission_notification'
          payload: { type: string; payload: { tool_call_id: string } }
      }
    | { type: string; payload: any }

const stripTrailingSlash = (url: string) => url.replace(/\/+$/, '')

export class CrushApi {
    readonly baseUrl: string

    constructor(baseUrl: string) {
        this.baseUrl = stripTrailingSlash(baseUrl)
    }

    async listWorkspaces(): Promise<Workspace[]> {
        return this.get('/v1/workspaces')
    }

    async listSessions(workspaceID: string): Promise<Session[]> {
        return this.get(`/v1/workspaces/${workspaceID}/sessions`)
    }

    async createSession(workspaceID: string): Promise<Session> {
        return this.post(`/v1/workspaces/${workspaceID}/sessions`, {
            title: `手机会话 ${new Date().toLocaleTimeString()}`,
        })
    }

    async listMessages(workspaceID: string, sessionID: string): Promise<Message[]> {
        return this.get(`/v1/workspaces/${workspaceID}/sessions/${sessionID}/messages`)
    }

    async getAgentInfo(workspaceID: string): Promise<AgentInfo> {
        return this.get(`/v1/workspaces/${workspaceID}/agent`)
    }

    async sendMessage(workspaceID: string, sessionID: string, prompt: string): Promise<void> {
        await this.post(`/v1/workspaces/${workspaceID}/agent`, {
            session_id: sessionID,
            prompt: prompt,
        })
    }

    async cancelSession(workspaceID: string, sessionID: string): Promise<void> {
        await this.post(`/v1/workspaces/${workspaceID}/agent/sessions/${sessionID}/cancel`, {})
    }

    async grantPermission(
        workspaceID: string,
        permission: PermissionRequest,
        action: 'allow' | 'allow_session' | 'deny'
    ): Promise<void> {
        await this.post(`/v1/workspaces/${workspaceID}/permissions/grant`, {
            permission,
            action,
        })
    }

    subscribeWorkspaceEvents(
        workspaceID: string,
        onEvent: (envelope: CrushEnvelope) => void,
        onError?: (err: Error) => void
    ) {
        const controller = new AbortController()
        const decoder = new TextDecoder()
        let buffer = ''

        expoFetch(`${this.baseUrl}/v1/workspaces/${workspaceID}/events`, {
            method: 'GET',
            headers: { Accept: 'text/event-stream' },
            signal: controller.signal,
        })
            .then(async (res) => {
                if (!res.ok || !res.body) throw new Error(`SSE ${res.status}`)
                const reader = res.body.getReader()
                while (true) {
                    const { done, value } = await reader.read()
                    if (done) break
                    buffer += decoder.decode(value, { stream: true })
                    const chunks = buffer.split('\n\n')
                    buffer = chunks.pop() || ''
                    for (const chunk of chunks) {
                        const raw = parseSSEData(chunk)
                        if (!raw) continue
                        try {
                            onEvent(JSON.parse(raw))
                        } catch (error) {
                            console.error('Failed to parse Crush SSE event', error)
                        }
                    }
                }
            })
            .catch((error) => {
                if (!controller.signal.aborted) {
                    console.error('Crush SSE failed', error)
                    onError?.(error instanceof Error ? error : new Error(String(error)))
                }
            })

        return () => controller.abort()
    }

    private async get<T>(path: string): Promise<T> {
        const res = await fetch(`${this.baseUrl}${path}`)
        if (!res.ok) throw new Error(`GET ${path}: ${res.status}`)
        return res.json()
    }

    private async post<T>(path: string, body: unknown): Promise<T> {
        const res = await fetch(`${this.baseUrl}${path}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        })
        if (!res.ok) throw new Error(`POST ${path}: ${res.status}`)
        const text = await res.text()
        if (!text) return undefined as T
        return JSON.parse(text)
    }
}

const parseSSEData = (chunk: string) => {
    return chunk
        .split('\n')
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice('data:'.length).trimStart())
        .join('\n')
        .trim()
}
