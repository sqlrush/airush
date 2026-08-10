// Package migrations 内嵌全部控制面 SQL 迁移（spec-0.6 D1）。
// 文件规范见 specs/spec-0.6-db-schema-migration.md §2.1：
// NNNN_<snake_case>.up.sql / .down.sql，已合并 main 的迁移不可修改（CI 强制）。
package migrations

import "embed"

// FS 是迁移文件的嵌入文件系统，由 internal/dbmigrate 经 iofs 消费。
//
//go:embed *.sql
var FS embed.FS
