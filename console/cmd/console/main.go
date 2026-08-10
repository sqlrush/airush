// Command console 是控制面入口。当前提供 migrate 子命令（spec-0.6）；
// API 服务随 spec-1.1 实装。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
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

	if args := flag.Args(); len(args) > 0 && args[0] == "migrate" {
		if err := dbmigrate.Run(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Println(banner(version))
}
