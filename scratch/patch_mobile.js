const fs = require('fs');
const path = require('path');

const targetFile = path.join(__dirname, '../mobile/crush_mobile/app/index.tsx');
let content = fs.readFileSync(targetFile, 'utf8');

console.log('Original content length:', content.length);

// 1. 修复由于刚才 replace_file_content 产生的截断，完整写回 MarkdownText 以及 ThinkingPart
// 我们先定位到 MarkdownText 渲染完成后的位置。
// MarkdownText 渲染以 `return (...)` 结尾，然后是 `const ToolCallPart`。
// 我们在 content 中替换那部分受损的代码。

const damagedPattern = `                const lines = block.split('\\n')
                return (
                    <View key={blockIdx} style={styles.markdownTextBlock}>
                        {lines.map((line, lineIdx) => {
                            const headingMatch = line.match(/^(#{1,6})\\s+(.*)$/)
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

                            const blockquoteMatch = line.match(/^>\\s*(.*)$/)
                            if (blockquoteMatch) {
                                const content = blockquoteMatch[1]
                                return (
                                    <View key={lineIdx} style={styles.markdownBlockquote}>
                                        <Text style={styles.markdownBlockquoteText}>
                                            {renderInline(content)}
                                        </Text>
                                    </View>
                                )
                            }

                            if (line === '---' || line === '***' || line === '___') {
                                return <View key={lineIdx} style={styles.markdownHR} />
                            }

                            const listMatch = line.match(/^(\\s*)[-*+]\\s+(.*)$/)
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

                            const numListMatch = line.match(/^(\\s*)(\\d+)\\.\\s+(.*)$/)
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

const ToolCallPart = ({`;

const fixedPattern = `                const lines = block.split('\\n')
                return (
                    <View key={blockIdx} style={styles.markdownTextBlock}>
                        {lines.map((line, lineIdx) => {
                            const headingMatch = line.match(/^(#{1,6})\\s+(.*)$/)
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

                            const blockquoteMatch = line.match(/^>\\s*(.*)$/)
                            if (blockquoteMatch) {
                                const content = blockquoteMatch[1]
                                return (
                                    <View key={lineIdx} style={styles.markdownBlockquote}>
                                        <Text style={styles.markdownBlockquoteText}>
                                            {renderInline(content)}
                                        </Text>
                                    </View>
                                )
                            }

                            if (line === '---' || line === '***' || line === '___') {
                                return <View key={lineIdx} style={styles.markdownHR} />
                            }

                            const listMatch = line.match(/^(\\s*)[-*+]\\s+(.*)$/)
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

                            const numListMatch = line.match(/^(\\s*)(\\d+)\\.\\s+(.*)$/)
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
}

// Sub-component for Collapsible Thinking Process
const ThinkingPart = ({ thinking }: { thinking: string }) => {
    const [expanded, setExpanded] = useState(false)
    if (!thinking) return null

    const toggle = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }

    return (
        <View style={styles.thinkingContainer}>
            <Pressable
                onPress={toggle}
                style={({ pressed }) => [styles.thinkingHeader, pressed && styles.pressed]}>
                <Feather
                    name={expanded ? 'chevron-down' : 'chevron-right'}
                    size={14}
                    color="#a78bfa"
                />
                <Text style={styles.thinkingHeaderText}>思考过程 (Thinking)...</Text>
            </Pressable>
            {expanded && (
                <View style={styles.thinkingBody}>
                    <Text style={styles.thinkingText}>{thinking}</Text>
                </View>
            )}
        </View>
    )
}

const ToolCallPart = ({`;

content = content.replace(damagedPattern, fixedPattern);

// 2. 为 ToolResultPart 增加 LayoutAnimation 支持
const toolResultTarget = `                {needsTruncation && (
                    <Pressable
                        onPress={() => setExpanded(!expanded)}
                        style={({ pressed }) => [
                            styles.expandCodeButton,
                            pressed && styles.pressed,
                        ]}>
                        <Text style={styles.expandCodeText}>
                            {expanded ? '收起输出' : \`展开余下 \${lines.length - 12} 行...\`}
                        </Text>
                    </Pressable>
                )}`;

const toolResultReplacement = `                {needsTruncation && (
                    <Pressable
                        onPress={toggle}
                        style={({ pressed }) => [
                            styles.expandCodeButton,
                            pressed && styles.pressed,
                        ]}>
                        <Text style={styles.expandCodeText}>
                            {expanded ? '收起输出' : \`展开余下 \${lines.length - 12} 行...\`}
                        </Text>
                    </Pressable>
                )}`;

// 在 ToolResultPart 中注入 toggle 函数
content = content.replace(
    `const ToolResultPart = ({ content, isError }: { content?: string; isError?: boolean }) => {
    const [expanded, setExpanded] = useState(false)`,
    `const ToolResultPart = ({ content, isError }: { content?: string; isError?: boolean }) => {
    const [expanded, setExpanded] = useState(false)

    const toggle = () => {
        LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut)
        setExpanded(!expanded)
    }`
);
content = content.replace(toolResultTarget, toolResultReplacement);

// 3. 修改 CrushMobile 组件以支持多 Workspace 轮询与 Drawer 手势
// 我们需要把 state 添加进去
const stateInsertionTarget = `    const [serverUrl, setServerUrl] = useState(DEFAULT_SERVER_URL)
    const [connectedUrl, setConnectedUrl] = useState(DEFAULT_SERVER_URL)
    const [workspaces, setWorkspaces] = useState<Workspace[]>([])
    const [workspaceID, setWorkspaceID] = useState('')
    const [sessions, setSessions] = useState<Session[]>([])
    const [sessionID, setSessionID] = useState('')
    const [messages, setMessages] = useState<Message[]>([])
    const [agentInfo, setAgentInfo] = useState<AgentInfo>({ is_busy: false, is_ready: false })
    const [pendingPermissions, setPendingPermissions] = useState<PermissionRequest[]>([])
    const [activities, setActivities] = useState<ActivityEntry[]>([])
    const [input, setInput] = useState('')
    const [status, setStatus] = useState('未连接')
    const [errorText, setErrorText] = useState('')
    const [isLoading, setIsLoading] = useState(false)
    const [showConnectionSettings, setShowConnectionSettings] = useState(false)`;

const stateInsertionReplacement = `    const [serverUrl, setServerUrl] = useState(DEFAULT_SERVER_URL)
    const [connectedUrl, setConnectedUrl] = useState(DEFAULT_SERVER_URL)
    const [workspaces, setWorkspaces] = useState<Workspace[]>([])
    const [workspaceID, setWorkspaceID] = useState('')
    const [sessions, setSessions] = useState<Session[]>([])
    const [sessionID, setSessionID] = useState('')
    const [messages, setMessages] = useState<Message[]>([])
    const [agentInfo, setAgentInfo] = useState<AgentInfo>({ is_busy: false, is_ready: false })
    const [pendingPermissions, setPendingPermissions] = useState<PermissionRequest[]>([])
    const [activities, setActivities] = useState<ActivityEntry[]>([])
    const [input, setInput] = useState('')
    const [status, setStatus] = useState('未连接')
    const [errorText, setErrorText] = useState('')
    const [isLoading, setIsLoading] = useState(false)
    const [showConnectionSettings, setShowConnectionSettings] = useState(false)

    // 新增：工作区和会话缓存状态，以便在抽屉里实时展示
    const [workspacesAgentInfo, setWorkspacesAgentInfo] = useState<Record<string, AgentInfo>>({})
    const [workspacesSessions, setWorkspacesSessions] = useState<Record<string, Session[]>>({})
    
    // 新增：手势控制 Drawer 的相关状态与动画
    const drawerTranslateX = useRef(new Animated.Value(-DRAWER_WIDTH)).current
    const [drawerOpen, setDrawerOpen] = useState(false)

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
                    // 从左侧边缘划入唤出
                    return x0 < 45 && dx > 8 && isHorizontal
                }
                // 在抽屉展开时，任意向左滑动都跟手
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
    ).current`;

content = content.replace(stateInsertionTarget, stateInsertionReplacement);

// 4. 实现 Workspace 选择逻辑，以便用户点击抽屉项时流畅切换
const selectWorkspaceFunc = `    const selectWorkspace = useCallback(async (targetWorkspaceID: string) => {
        try {
            setWorkspaceID(targetWorkspaceID)
            const nextSessions = await api.listSessions(targetWorkspaceID)
            setSessions(nextSessions)
            if (nextSessions.length > 0) {
                setSessionID(nextSessions[0].id)
            } else {
                const created = await api.createSession(targetWorkspaceID)
                setSessions([created])
                setSessionID(created.id)
            }
            refreshAgent(targetWorkspaceID)
        } catch (error) {
            console.error('Failed to select workspace', error)
        }
    }, [api, refreshAgent])`;

// 把这个函数插入到 `connect` 函数定义之前
content = content.replace(
    `    const connect = useCallback(async () => {`,
    selectWorkspaceFunc + '\n\n' + `    const connect = useCallback(async () => {`
);

// 5. 增加 workspaces 后台轮询效应以捕获在别的 pwd 启动的 crush 进程
const workspacesPollingEffect = `    // 增加：后台轮询所有 Workspace 与会话列表，实现双向状态切换与同步
    useEffect(() => {
        let active = true
        const poll = async () => {
            const isConnectedNow = status === '在线' || status === '订阅事件'
            if (!isConnectedNow) return
            try {
                const nextWorkspaces = await api.listWorkspaces()
                if (!active) return
                setWorkspaces(nextWorkspaces)

                const infoMap: Record<string, AgentInfo> = {}
                const sessMap: Record<string, Session[]> = {}

                await Promise.all(
                    nextWorkspaces.map(async (w) => {
                        try {
                            const info = await api.getAgentInfo(w.id)
                            const sess = await api.listSessions(w.id)
                            infoMap[w.id] = info
                            sessMap[w.id] = sess
                        } catch (e) {
                            // 忽略单个工作区拉取失败
                        }
                    })
                )

                if (!active) return
                setWorkspacesAgentInfo(infoMap)
                setWorkspacesSessions(sessMap)
            } catch (err) {
                console.error('Failed to poll workspaces metadata', err)
            }
        }

        poll()
        const timer = setInterval(poll, 5000)
        return () => {
            active = false
            clearInterval(timer)
        }
    }, [api, status])`;

// 插入在 `useEffect(() => { connect() }, [connect])` 下面
content = content.replace(
    `    useEffect(() => {
        connect()
    }, [connect])`,
    `    useEffect(() => {
        connect()
    }, [connect])\n\n` + workspacesPollingEffect
);

// 6. 改造 Header 和主渲染布局。 
// 首先是 Header，在 Header 顶层，点击 `Crush` 标题或者 Workspace badge 都可以打开抽屉。
// 我们在 `headerTop` 包装 Pressable：
// 还要删去原来的 sessionRail `styles.sessionRail`
// 我们定位到 JSX 的返回结构：
const originalReturnPattern = `    return (
        <SafeAreaView style={styles.safeArea}>
            <KeyboardAvoidingView
                behavior={Platform.OS === 'ios' ? 'padding' : undefined}
                style={styles.root}>
                <View style={styles.header}>
                    <View style={styles.headerTop}>
                        <View style={styles.titleContainer}>
                            <Text style={styles.title}>Crush</Text>
                            <View
                                style={[
                                    styles.statusDot,
                                    agentInfo.is_busy
                                        ? styles.statusDotBusy
                                        : isConnected
                                          ? styles.statusDotOnline
                                          : styles.statusDotOffline,
                                ]}
                            />
                            <Text style={styles.statusText}>
                                {agentInfo.is_busy ? '运行中' : status}
                            </Text>
                        </View>`;

const animatedStatusDotReplacement = `    return (
        <SafeAreaView style={styles.safeArea} {...panResponder.panHandlers}>
            <KeyboardAvoidingView
                behavior={Platform.OS === 'ios' ? 'padding' : undefined}
                style={styles.root}>
                <View style={styles.header}>
                    <View style={styles.headerTop}>
                        <Pressable 
                            style={({ pressed }) => [styles.titleContainer, pressed && styles.pressed]}
                            onPress={openDrawer}>
                            <Feather name="menu" size={18} color="#f8fafc" style={{ marginRight: 2 }} />
                            <Text style={styles.title}>Crush</Text>
                            <Animated.View
                                style={[
                                    styles.statusDot,
                                    agentInfo.is_busy
                                        ? styles.statusDotBusy
                                        : isConnected
                                          ? styles.statusDotOnline
                                          : styles.statusDotOffline,
                                    {
                                        opacity: agentInfo.is_busy || isConnected ? breatheAnim : 1.0
                                    }
                                ]}
                            />
                            <Text style={styles.statusText}>
                                {agentInfo.is_busy ? '运行中' : status}
                            </Text>
                        </Pressable>`;

content = content.replace(originalReturnPattern, animatedStatusDotReplacement);

// 7. 把 Workspace Badge 点击事件也绑定为打开抽屉
content = content.replace(
    `<View style={styles.compactMetaRow}>
                        <View style={styles.metaBadge}>`,
    `<View style={styles.compactMetaRow}>
                        <Pressable style={styles.metaBadge} onPress={openDrawer}>`
);
content = content.replace(
    `</View>
                        <View style={styles.metaBadge}>`,
    `</Pressable>
                        <View style={styles.metaBadge}>`
);

// 8. 移除原来的 sessionRail 视图并嵌入 Drawer 组件
// 原本是：
// `<View style={styles.content}>
//     <View style={styles.sessionRail}> ... </View>
//     {activities.length > 0 && ...`
// 我们将其替换为直接显示 activities 以及在底部加入绝对定位的 Drawer 和 Backdrop 视图。

const contentPattern = `                <View style={styles.content}>
                    <View style={styles.sessionRail}>
                        <FlatList
                            horizontal
                            data={sessions}
                            keyExtractor={(item) => item.id}
                            showsHorizontalScrollIndicator={false}
                            ListHeaderComponent={
                                <Pressable
                                    style={({ pressed }) => [
                                        styles.newSessionChip,
                                        pressed && styles.pressed,
                                    ]}
                                    disabled={!workspaceID || agentInfo.is_busy}
                                    onPress={createNewSession}>
                                    <Feather name="plus" size={12} color="#34d399" />
                                    <Text style={styles.newSessionChipText}>新会话</Text>
                                </Pressable>
                            }
                            renderItem={({ item }) => (
                                <Pressable
                                    style={[
                                        styles.sessionChip,
                                        item.id === sessionID && styles.sessionChipActive,
                                    ]}
                                    onPress={() => setSessionID(item.id)}>
                                    <Text
                                        style={[
                                            styles.sessionChipText,
                                            item.id === sessionID && styles.sessionChipTextActive,
                                        ]}
                                        numberOfLines={1}>
                                        {item.title}
                                    </Text>
                                </Pressable>
                            )}
                        />
                    </View>`;

const newContentReplacement = `                <View style={styles.content}>`;

content = content.replace(contentPattern, newContentReplacement);

// 在最外层容器结束前插入 Drawer UI。
// 让我们在 `</KeyboardAvoidingView>` 的上方插入 Drawer UI。
const drawerUI = `                {/* Drawer 阴影遮罩层 */}
                {drawerOpen && (
                    <Animated.View
                        style={[
                            styles.drawerBackdrop,
                            { opacity: backdropOpacity },
                        ]}>
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
                                    { opacity: isConnected ? breatheAnim : 1.0 }
                                ]}
                            />
                        </View>
                        <Text style={styles.drawerHeaderSub} numberOfLines={1}>
                            服务地址: {connectedUrl}
                        </Text>
                    </View>

                    <ScrollView style={styles.drawerScroll} showsVerticalScrollIndicator={false}>
                        <Text style={styles.drawerSectionTitle}>工作区与会话列表</Text>
                        
                        {workspaces.length === 0 ? (
                            <View style={{ paddingHorizontal: 20, paddingVertical: 10 }}>
                                <Text style={{ color: '#4b5563', fontSize: 12 }}>无工作区</Text>
                            </View>
                        ) : (
                            workspaces.map((w) => {
                                const isActiveWorkspace = w.id === workspaceID;
                                const isWorkspaceBusy = workspacesAgentInfo[w.id]?.is_busy;
                                const wsSessions = workspacesSessions[w.id] || [];
                                
                                return (
                                    <View key={w.id} style={styles.workspaceNode}>
                                        {/* 工作区行 */}
                                        <Pressable
                                            style={[
                                                styles.workspaceItem,
                                                isActiveWorkspace && styles.workspaceItemActive,
                                            ]}
                                            onPress={() => selectWorkspace(w.id)}>
                                            <View style={styles.workspaceLeft}>
                                                <Feather 
                                                    name="folder" 
                                                    size={13} 
                                                    color={isActiveWorkspace ? '#38bdf8' : '#64748b'} 
                                                />
                                                <Text 
                                                    style={[
                                                        styles.workspaceName,
                                                        isActiveWorkspace && styles.workspaceNameActive,
                                                    ]}
                                                    numberOfLines={1}>
                                                    {w.path.split('/').filter(Boolean).pop() || w.path}
                                                </Text>
                                            </View>
                                            
                                            {/* 工作区状态呼吸灯 */}
                                            <Animated.View
                                                style={[
                                                    styles.statusDot,
                                                    isWorkspaceBusy
                                                        ? styles.statusDotBusy
                                                        : isActiveWorkspace && isConnected
                                                          ? styles.statusDotOnline
                                                          : styles.statusDotOffline,
                                                    {
                                                        opacity: isWorkspaceBusy || (isActiveWorkspace && isConnected) ? breatheAnim : 1.0
                                                    }
                                                ]}
                                            />
                                        </Pressable>

                                        {/* 会话展开列表（仅对当前活跃的 Workspace 展开） */}
                                        {isActiveWorkspace && (
                                            <View style={styles.sessionsContainer}>
                                                {wsSessions.map((s) => {
                                                    const isActiveSession = s.id === sessionID;
                                                    return (
                                                        <Pressable
                                                            key={s.id}
                                                            style={[
                                                                styles.sessionNode,
                                                                isActiveSession && styles.sessionNodeActive,
                                                            ]}
                                                            onPress={() => {
                                                                setSessionID(s.id);
                                                                closeDrawer();
                                                            }}>
                                                            <Text
                                                                style={[
                                                                    styles.sessionNodeText,
                                                                    isActiveSession && styles.sessionNodeTextActive,
                                                                ]}
                                                                numberOfLines={1}>
                                                                {s.title}
                                                            </Text>
                                                            {isActiveSession && (
                                                                <Feather name="check" size={12} color="#f0f9ff" />
                                                            )}
                                                        </Pressable>
                                                    );
                                                })}
                                                
                                                {/* 创建新会话入口 */}
                                                <Pressable
                                                    style={({ pressed }) => [
                                                        styles.drawerNewSessionBtn,
                                                        pressed && styles.pressed,
                                                    ]}
                                                    disabled={agentInfo.is_busy}
                                                    onPress={createNewSession}>
                                                    <Feather name="plus" size={11} color="#a7f3d0" />
                                                    <Text style={styles.drawerNewSessionText}>新会话</Text>
                                                </Pressable>
                                            </View>
                                        )}
                                    </View>
                                );
                            })
                        )}
                    </ScrollView>

                    <View style={styles.drawerFooter}>
                        <Text style={styles.drawerFooterLabel}>当前大语言模型</Text>
                        <Text style={styles.drawerFooterVal} numberOfLines={1}>
                            {modelName}
                        </Text>
                    </View>
                </Animated.View>
            </KeyboardAvoidingView>`;

content = content.replace(`            </KeyboardAvoidingView>`, drawerUI);

// 9. 添加 StyleSheet styles，在文件尾部进行扩展
// 我们把所有新增样式加入到 styles 的对象定义中。
const styleInsertIndex = content.lastIndexOf('})');
const additionalStyles = `
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
        alignItems: 'center',
        justifyContent: 'space-between',
        height: 36,
        paddingHorizontal: 12,
        borderRadius: 6,
    },
    sessionNodeActive: {
        backgroundColor: '#0c4a6e',
    },
    sessionNodeText: {
        color: '#64748b',
        fontSize: 12.5,
        fontWeight: '600',
        flex: 1,
    },
    sessionNodeTextActive: {
        color: '#f0f9ff',
        fontWeight: '700',
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
`;

content = content.slice(0, styleInsertIndex) + additionalStyles + content.slice(styleInsertIndex);

fs.writeFileSync(targetFile, content, 'utf8');
console.log('Successfully patched index.tsx. New content length:', content.length);
