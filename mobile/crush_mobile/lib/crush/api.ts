import 'react-native-get-random-values'
import { connect, consumerOpts, JSONCodec, NatsConnection } from 'nats.ws'

import { dlog } from './dlog'

export type Session = {
    id: string
    title: string
    message_count?: number
    updated_at?: number
    created_at?: number
    path?: string
    alive?: boolean
    /** Provider id of the brain agent's current model, as reported by the
     *  TUI presence record. Refreshed each heartbeat so changes from
     *  `set_model` show up within ~5s. */
    provider?: string
    /** Model id (e.g. "gpt-5.4-mini") for the brain agent. */
    model?: string
    models?: Record<string, { provider: string; model: string }>
    available_models?: { provider: string; model: string }[]
    is_busy?: boolean
}

export type MessagePart = {
    type: 'text' | 'reasoning' | 'tool_call' | 'tool_result' | 'finish' | string
    data: {
        text?: string
        thinking?: string
        reason?: string
        message?: string
        details?: string
        time?: number
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
    | { type: 'sync_complete'; payload: { session_id: string } }
    | { type: string; payload: any }

export type HistoryPage = {
    messages: Message[]
    exhausted: boolean
}

const jc = JSONCodec<any>()

function looksLikeEmptySessionTitle(title: string | undefined, sessionID: string): boolean {
    const cleaned = (title || '').trim()
    if (!cleaned) return true
    if (cleaned === sessionID) return true
    if (cleaned === 'Untitled Session' || cleaned === '未命名会话') return true
    if (cleaned.startsWith('title-') || cleaned.includes('$$')) return true
    return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(cleaned)
}

function shouldShowSession(session: Session): boolean {
    if (!session.id) return false
    if (!looksLikeEmptySessionTitle(session.title, session.id)) return true
    return !!session.is_busy || (session.message_count || 0) > 0
}

export class CrushApi {
    private nc: NatsConnection | null = null
    readonly url: string
    readonly token: string

    constructor(url: string, token: string = 'ymm_rpc_2026') {
        this.url = url
        this.token = token
    }

    async connect(): Promise<NatsConnection> {
        if (this.nc) return this.nc
        dlog('[CrushApi] connecting to', this.url)
        const t0 = Date.now()
        this.nc = await connect({
            servers: this.url,
            token: this.token,
            name: 'crush-mobile',
            waitOnFirstConnect: true,
            reconnect: true,
        })
        dlog(`[TRACE] nats_connect +${Date.now() - t0}ms url=${this.url}`)
        return this.nc
    }

    async listSessions(onUpdate: (sessions: Session[]) => void): Promise<() => void> {
        const nc = await this.connect()
        const kv = await nc.jetstream().views.kv('CRUSH_SESSIONS')
        const sessionsMap: Record<string, Session> = {}

        const emit = () => {
            // Sort by created_at (stable, set once at session birth) instead
            // of updated_at — otherwise every 5s heartbeat reorders the list
            // and visually swaps neighbouring sessions back and forth.
            onUpdate(
                Object.values(sessionsMap)
                    .filter(shouldShowSession)
                    .sort((a, b) => {
                        const ta = a.created_at || a.updated_at || 0
                        const tb = b.created_at || b.updated_at || 0
                        if (tb !== ta) return tb - ta
                        return (a.id || '').localeCompare(b.id || '')
                    })
            )
        }

        const iter = await kv.watch({ key: '*' })
        let stopped = false
        ;(async () => {
            for await (const e of iter) {
                if (e.operation === 'DEL' || e.operation === 'PURGE') {
                    delete sessionsMap[e.key]
                } else {
                    try {
                        const meta = jc.decode(e.value)
                        sessionsMap[e.key] = {
                            id: meta.session_id,
                            title: meta.title || meta.session_id,
                            message_count: meta.message_count,
                            path: meta.path,
                            alive: meta.alive,
                            updated_at: meta.updated_at,
                            created_at: meta.created_at,
                            provider: meta.provider,
                            model: meta.model,
                            models: meta.models,
                            available_models: meta.available_models,
                            is_busy: meta.is_busy,
                        }
                    } catch (err) {
                        console.error('Failed to decode session meta', err)
                    }
                }
                emit()
            }
        })()

        // NATS KV TTL expiry does not reliably emit DEL events to the
        // nats.ws watch iterator. Reconcile every 8s by listing live
        // keys and dropping any sessionsMap entry whose key has vanished
        // server-side. This also flips entries to alive=false when the
        // backing TUI hasn't heart-beated within ~2× heartbeat (10s).
        const reconcileIntervalMs = 8000
        const staleAfterMs = 12_000
        const reconcile = async () => {
            if (stopped) return
            try {
                const keysIter = await kv.keys()
                const liveKeys = new Set<string>()
                for await (const k of keysIter) liveKeys.add(k)
                let changed = false
                for (const id of Object.keys(sessionsMap)) {
                    if (!liveKeys.has(id)) {
                        delete sessionsMap[id]
                        changed = true
                        continue
                    }
                    const s = sessionsMap[id]
                    const ageMs = Date.now() - (s.updated_at || 0) * 1000
                    if (s.alive !== false && ageMs > staleAfterMs) {
                        sessionsMap[id] = { ...s, alive: false }
                        changed = true
                    }
                }
                if (changed) emit()
            } catch (err) {
                console.error('[CrushApi] reconcile sessions failed', err)
            }
        }
        const reconcileTimer = setInterval(reconcile, reconcileIntervalMs)
        // Fire one reconcile after initial watch settle so on cold start
        // the list reflects current presence (not just historical PUTs).
        setTimeout(reconcile, 1500)

        return () => {
            stopped = true
            clearInterval(reconcileTimer)
            iter.stop()
        }
    }

    async subscribeSessionEvents(
        sessionID: string,
        onEvent: (envelope: CrushEnvelope) => void,
        onError?: (err: Error) => void,
        opts2?: { historyMs?: number; sinceTs?: number }
    ): Promise<() => void> {
        const nc = await this.connect()
        const js = nc.jetstream()

        const subject = `crush.sess.${sessionID}.events`
        // Live streaming deltas arrive over an ephemeral core-NATS subject the
        // relay publishes to per token-debounce tick. JetStream (.events) now
        // stores ONLY terminal message snapshots (finished assistant /
        // complete user|tool / deletions), so a cold-open replay no longer
        // carries every intermediate frame. We must subscribe to .live too or
        // an attached session would only refresh on each finished message.
        const liveSubject = `crush.sess.${sessionID}.live`
        const liveSub = nc.subscribe(liveSubject)
        ;(async () => {
            try {
                for await (const m of liveSub) {
                    try {
                        onEvent(jc.decode(m.data))
                    } catch (err) {
                        console.error('Failed to decode live event', err)
                    }
                }
            } catch (err) {
                // A live-subject error is non-fatal: durable .events still
                // delivers terminal snapshots. Log and let the JetStream sub
                // drive onError for connection-level failures.
                dlog('Live subject subscription ended', err)
            }
        })()
        try {
            const opts = consumerOpts()
            opts.orderedConsumer().filterSubject(subject)
            // Default = full session history (deliverAll). A time window
            // looks attractive ("cap to last N min") but breaks the common
            // case where the user opens a paused session whose last event
            // is older than the window — the ordered consumer then sits
            // empty forever and the UI stays blank. JetStream now holds only
            // terminal snapshots so cold-open replay is bounded by message
            // count, not per-token frames. Pass historyMs explicitly only if
            // the caller really wants to cap.
            if (opts2?.sinceTs && opts2.sinceTs > 0) {
                // Reopening a locally cached session only needs events after
                // the cached tail. Include a small overlap for streaming
                // updates to the final cached message; map dedupe absorbs it.
                opts.startTime(new Date(Math.max(0, opts2.sinceTs - 1000)))
            } else if (opts2?.historyMs && opts2.historyMs > 0) {
                opts.startAtTimeDelta(opts2.historyMs)
            } else {
                // Cold open has no local cache, so replay the session stream.
                // React flush batching handles large replays; later opens use
                // sinceTs to fetch only the tail after the local cache.
                opts.deliverAll()
            }
            const sub = await js.subscribe(subject, opts)

            ;(async () => {
                try {
                    for await (const m of sub) {
                        try {
                            onEvent(jc.decode(m.data))
                            // If pending is 0, we've caught up with history.
                            if (m.info?.pending === 0) {
                                onEvent({
                                    type: 'sync_complete',
                                    payload: { session_id: sessionID },
                                })
                            }
                        } catch (err) {
                            console.error('Failed to decode event', err)
                        }
                    }
                } catch (err) {
                    onError?.(err as Error)
                }
            })()

            return () => {
                try {
                    sub.unsubscribe()
                } catch {
                    // ignore
                }
                try {
                    liveSub.unsubscribe()
                } catch {
                    // ignore
                }
            }
        } catch (err) {
            console.error('Failed to create JetStream consumer', err)
            onError?.(err as Error)
            return () => {
                try {
                    liveSub.unsubscribe()
                } catch {
                    // ignore
                }
            }
        }
    }

    /**
     * Fetches one older page from the active TUI relay's authoritative local
     * message store. JetStream cannot serve as a history cursor because DB
     * backfill republishes old messages with current stream timestamps.
     */
    async loadHistoryBefore(
        sessionID: string,
        beforeMessageID: string,
        beforeCreatedAt: number,
        limit: number = 50
    ): Promise<HistoryPage> {
        const nc = await this.connect()
        const response = await nc.request(
            `crush.sess.${sessionID}.cmd`,
            jc.encode({
                type: 'history_before',
                before_message_id: beforeMessageID,
                before_created_at: beforeCreatedAt,
                limit,
            }),
            { timeout: 5000 }
        )
        const page = jc.decode(response.data) as HistoryPage
        if (!Array.isArray(page.messages) || typeof page.exhausted !== 'boolean') {
            throw new Error('loadHistoryBefore: invalid relay history response')
        }
        return page
    }

    async sendMessage(sessionID: string, prompt: string): Promise<void> {
        const nc = await this.connect()
        nc.publish(`crush.sess.${sessionID}.cmd`, jc.encode({ type: 'prompt', text: prompt }))
    }

    async cancelSession(sessionID: string): Promise<void> {
        const nc = await this.connect()
        nc.publish(`crush.sess.${sessionID}.cmd`, jc.encode({ type: 'cancel' }))
    }

    async deleteSession(sessionID: string): Promise<void> {
        const nc = await this.connect()
        nc.publish(`crush.sess.${sessionID}.cmd`, jc.encode({ type: 'kill' }))
        try {
            const kv = await nc.jetstream().views.kv('CRUSH_SESSIONS')
            await kv.delete(sessionID)
        } catch (err) {
            console.error(`[CrushApi] failed to delete session key ${sessionID} from KV`, err)
        }
    }

    async grantPermission(
        sessionID: string,
        toolCallID: string,
        action: 'allow' | 'deny'
    ): Promise<void> {
        const nc = await this.connect()
        nc.publish(
            `crush.sess.${sessionID}.cmd`,
            jc.encode({ type: 'grant', tool_call_id: toolCallID, action })
        )
    }

    /**
     * Persist a model selection in the TUI for the given agent role. The TUI
     * relay handler writes models.<role>.{provider,model} into the local
     * state.yaml (auto-routed by isStateKey), so the next dispatch picks up
     * the new model. Role is one of "brain" / "worker" / "explore".
     */
    async setModel(
        sessionID: string,
        role: 'brain' | 'worker' | 'explore',
        provider: string,
        model: string
    ): Promise<void> {
        const nc = await this.connect()
        nc.publish(
            `crush.sess.${sessionID}.cmd`,
            jc.encode({ type: 'set_model', role, provider, model })
        )
    }
}
