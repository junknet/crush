filepath = "mobile/crush_mobile/app/index.tsx"

with open(filepath, "r", encoding="utf-8") as f:
    lines = f.readlines()

# We want to replace lines[3375:3385] (which is line 3376 to 3385 in 1-based index)
# Let's inspect the target lines first to make sure they match what we expect.
expected_start = "                                            <View style={styles.workspaceRight}>"
expected_end = "                                            </View>"

if lines[3375].strip() == expected_start.strip() and lines[3384].strip() == expected_end.strip():
    replacement = [
        '                                            <View style={styles.workspaceRight}>\n',
        '                                                {/* 工作区状态指示灯 */}\n',
        '                                                <Animated.View\n',
        '                                                    style={[\n',
        '                                                        styles.statusDot,\n',
        '                                                        isWorkspaceOnline\n',
        '                                                            ? styles.statusDotOnline\n',
        '                                                            : styles.statusDotOffline,\n',
        '                                                        {\n',
        '                                                            opacity: isWorkspaceOnline\n',
        '                                                                ? (isWorkspaceBusy ? blinkAnim : 1.0)\n',
        '                                                                : 1.0,\n',
        '                                                        },\n',
        '                                                    ]}\n',
        '                                                />\n',
        '                                                <Feather\n',
        '                                                    name={\n',
        '                                                        isExpanded\n',
        '                                                            ? \'chevron-down\'\n',
        '                                                            : \'chevron-right\'\n',
        '                                                    }\n',
        '                                                    size={14}\n',
        '                                                    color="#64748b"\n',
        '                                                    style={{ marginLeft: 6 }}\n',
        '                                                />\n',
        '                                            </View>\n'
    ]
    lines[3375:3385] = replacement
    with open(filepath, "w", encoding="utf-8") as f:
        f.writelines(lines)
    print("SUCCESS")
else:
    print("MISMATCH:")
    print("Start:", repr(lines[3375]))
    print("End:", repr(lines[3384]))
