// Case: deep_nest.go
// 目的：验证在存在多层完全相同的 for 循环嵌套结构下，Patcher 能否正确识别最内层的 println 并修改它，同时通过 reindent 保持缩进规整。
package d

func R() {
	for a := 0; a < 2; a++ {
		for b := 0; b < 2; b++ {
			for c := 0; c < 2; c++ {
				val := a + b + c
				println(val) // 目标修改点：改为 println(val * 10)
			}
		}
	}
}
