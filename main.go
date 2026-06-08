package main

import (
	"fmt"
	"os"

	"zey/cmd"
)

// main 是程序唯一入口。Go 约定:可执行程序的包名必须是 main,
// 且必须有一个无参数无返回值的 main 函数。
func main() {
	// 所有真正的逻辑都在 cmd 包里。这里只负责:执行命令,出错就打印并退出。
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1) // 非 0 退出码表示失败,方便 shell 脚本判断
	}
}
