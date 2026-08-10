// Command console is a scaffold placeholder (spec-0.1)：仅打印版本退出，
// 真实实现从对应 Stage 1 spec 起整体替换本文件。
package main

import (
	"flag"
	"fmt"
)

// version 由构建期 -ldflags 注入（spec-0.10/0.11 定版链路）。
var version = "0.0.0-dev"

// banner 组装启动横幅（spec-0.4 D5 范本的被测单元）。
func banner(v string) string {
	return "console " + v
}

func main() {
	flag.Bool("version", false, "print version and exit")
	flag.Parse()
	fmt.Println(banner(version))
}
