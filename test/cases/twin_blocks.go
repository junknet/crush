// Case: twin_blocks.go
// 目的：验证当文件中存在两个缩进、结构完全一致的函数（A 和 B）时，Patcher 能否结合 context-line 消除歧义，且在 whitespace-collapsed 下只修改 B。
package d

func A() {
	cfg := load()
	cfg.timeout = 30
	run(cfg)
}

func B() {
	cfg := load()
	cfg.timeout = 30 // 目标修改点：仅将 B 里的该行修改为 60
	run(cfg)
}
