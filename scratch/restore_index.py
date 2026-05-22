import os

filepath = "mobile/crush_mobile/app/index.tsx"

with open(filepath, "r", encoding="utf-8") as f:
    content = f.read()

target = """                                             <View style={styles.workspaceRight}>
                                                 {/* 工作区状态指示灯 */}
                                                 <Animated.View
                                                     style={[
                                                         styles.statusDot,
                                                         isWorkspaceOnline
                                                             ? styles.statusDotOnline
                                                             : styles.statusDotOffline,
                                                         {
                                             </View>"""

replacement = """                                             <View style={styles.workspaceRight}>
                                                 {/* 工作区状态指示灯 */}
                                                 <Animated.View
                                                     style={[
                                                         styles.statusDot,
                                                         isWorkspaceOnline
                                                             ? styles.statusDotOnline
                                                             : styles.statusDotOffline,
                                                         {
                                                             opacity: isWorkspaceOnline
                                                                 ? (isWorkspaceBusy ? blinkAnim : 1.0)
                                                                 : 1.0,
                                                         },
                                                     ]}
                                                 />
                                                 <Feather
                                                     name={
                                                         isExpanded
                                                             ? 'chevron-down'
                                                             : 'chevron-right'
                                                     }
                                                     size={14}
                                                     color="#64748b"
                                                     style={{ marginLeft: 6 }}
                                                 />
                                             </View>"""

if target in content:
    content = content.replace(target, replacement)
    with open(filepath, "w", encoding="utf-8") as f:
        f.write(content)
    print("SUCCESS")
else:
    print("TARGET NOT FOUND")
