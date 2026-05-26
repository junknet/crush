import 'react-native-get-random-values'
import { connect, consumerOpts, JSONCodec, NatsConnection } from 'nats.ws'

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
    | { type: 'permission_notification'; payload: { type: string; payload: { tool_call_id: string } } }
    | { type: 'sync_complete'; payload: { session_id: string } }
    | { type: string; payload: any }

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
        console.log('[CrushApi] connecting to', this.url)
        this.nc = await connect({
            servers: this.url,
            token: this.token,
            name: 'crush-mobile',
            waitOnFirstConnect: true,
            reconnect: true,
        })
        console.log('[CrushApi] connected')
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
        opts2?: { historyMs?: number }
    ): Promise<() => void> {
        const nc = await this.connect()
        const js = nc.jetstream()

        const subject = `crush.sess.${sessionID}.events`
        try {
            const opts = consumerOpts()
            opts.orderedConsumer().filterSubject(subject)
            // Default = full session history (deliverAll). A time window
            // looks attractive ("cap to last N min") but breaks the common
            // case where the user opens a paused session whose last event
            // is older than the window — the ordered consumer then sits
            // empty forever and the UI stays blank. Map dedupe + 100ms
            // batch + FlatList virtualization already handle the volume
            // (tested up to 11k events / 78MB). Pass historyMs explicitly
            // only if the caller really wants to cap.
            if (opts2?.historyMs && opts2.historyMs > 0) {
                opts.startAtTimeDelta(opts2.historyMs)
            } else {
                // Pull last 20 messages by default to ensure instant load.
                // loadOlderHistory handles backfilling older context on scroll-up.
                opts.deliverLast(20)
            }
            const sub = await js.subscribe(subject, opts)

            ;(async () => {
                try {
                    for await (const m of sub) {
                        try {
                            onEvent(jc.decode(m.data))
                            // If pending is 0, we've caught up with history.
                            if (m.info?.pending === 0) {
                                onEvent({ type: 'sync_complete', payload: { session_id: sessionID } })
                            }
                        } catch (err) {
                            console.error('Failed to decode event', err)
                        }
                    }
                } catch (err) {
                    onError?.(err as Error)
                }
            })()

            return () => sub.unsubscribe()
        } catch (err) {
            console.error('Failed to create JetStream consumer', err)
            onError?.(err as Error)
            return () => {}
        }
    }

    /**
     * One-shot pull of older history for infinite-scroll. Creates a temporary
     * ordered consumer starting at (untilTs - durationMs) and drains every
     * message whose stream timestamp is strictly before `untilTs`. Returns
     * once the historical window is exhausted (info.pending === 0) or a
     * message has crossed into the already-loaded range. Messages are pushed
     * through `onEvent` exactly like the live subscription so the caller's
     * existing Map dedupe + flush path handles ordering.
     *
     * untilTs is a unix-millis cutoff — typically the created_at of the
     * currently-oldest message on screen. durationMs is how far back to
     * extend (default 30 min, matching the initial live window).
     */
    async loadOlderHistory(
        sessionID: string,
        untilTs: number,
        durationMs: number,
        onEvent: (envelope: CrushEnvelope) => void
    ): Promise<number> {
        const nc = await this.connect()
        const js = nc.jetstream()
        const subject = `crush.sess.${sessionID}.events`
        const opts = consumerOpts()
        opts.orderedConsumer().filterSubject(subject)
        const startTs = Math.max(0, untilTs - durationMs)
        opts.startTime(new Date(startTs))
        const sub = await js.subscribe(subject, opts)
        let count = 0
        try {
            for await (const m of sub) {
                try {
                    const tsNanos = m.info?.timestampNanos ?? 0
                    const tsMs = tsNanos / 1e6
                    if (tsMs > 0 && tsMs >= untilTs) break
                    try {
                        onEvent(jc.decode(m.data))
                        count++
                    } catch (err) {
                        console.error('Failed to decode older event', err)
                    }
                    if (m.info?.pending === 0) break
                } catch (err) {
                    // m.info getter parses ack-subject; malformed reply
                    // tokens can throw. Skip the message rather than crash
                    // the RN bridge with an unhandled rejection.
                    console.log('loadOlderHistory msg info parse failed:', err)
                }
            }
        } finally {
            sub.unsubscribe()
        }
        return count
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
