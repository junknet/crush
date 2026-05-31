// Case: ambiguous_big.go
// 目的：验证在存在大量高度相似的函数块（F0 - F11）时，Patcher 能否精确根据 F7 的上下文定位并仅修改 F7，而不影响其他函数。
package d

func F0() int {
	x := 0
	return x
}

func F1() int {
	x := 1
	return x
}

func F2() int {
	x := 2
	return x
}

func F3() int {
	x := 3
	return x
}

func F4() int {
	x := 4
	return x
}

func F5() int {
	x := 5
	return x
}

func F6() int {
	x := 6
	return x
}

func F7() int {
	x := 7
	return x // 目标修改点：改为 return x * 2
}

func F8() int {
	x := 8
	return x
}

func F9() int {
	x := 9
	return x
}

func F10() int {
	x := 10
	return x
}

func F11() int {
	x := 11
	return x
}
