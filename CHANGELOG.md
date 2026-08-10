# Changelog

格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)；
版本策略见 specs/spec-0.11-versioning-release.md §2.1（平台统一版本，Stage 0 完成 = v0.1.0）。
每个改变行为的 PR 在 Unreleased 段追加条目（PR 模板检查项）；本文件是 release notes 唯一来源。

## [Unreleased]

### Added

- 控制面领域模型与 API 骨架（spec-1.1）：
  - 迁移 0002：users/connectors/datasource_groups/agents/datasource_credentials/
    datasources/datasource_aliases/idempotency_keys 八表，全部租户表套 RLS 模板四要素，
    租户内外键复合形态防跨租户悬挂引用；dev 租户 + dev 管理员 seed；
  - `console --serve`：控制面 REST API（datasources/agents/datasource-groups/aliases/
    connectors CRUD），OpenAPI 契约先行（proto/openapi/console.yaml）；
  - 租户上下文基座：tenancy ctx 唯一注入/取用点 + repo 租户事务
    （SET LOCAL ROLE airush_app + app.tenant_id，RLS 应用层执行路径）+
    depguard 硬禁 httpapi 直连 pgx；
  - 直连凭据信封加密（AD-4②）：AES-256-GCM 双层（KEK env/k8s Secret 注入、
    DEK 每凭据随机、key_id 轮换位）；API/日志/响应零回显；
  - keyset 分页（不透明游标）+ Idempotency-Key 幂等（响应快照同事务落库）；
  - 新错误码 7 个（AR_DATASOURCE_*/AR_AGENT_NOT_FOUND/AR_ALIAS_CONFLICT/
    AR_IDEMPOTENCY_REPLAY）；
  - Helm console 组件（Deployment/Service/KEK Secret，dev 默认开启）+
    dev-verify console API 端到端断言。

## [0.1.0] - 2026-08-10

### Added

- monorepo 脚手架与三语言构建体系：Go workspace 四组件、Python uv workspace、前端 Vite（spec-0.1）
- 三语言 lint 体系：golangci-lint v2（depguard 解耦边界）、ruff+mypy strict、eslint strict+prettier（spec-0.2）
- CI/CD：lint/test/build/integration 四 required checks + gitleaks 阻断 + 每日依赖扫描 + 分支保护（spec-0.3）
- 单元测试框架与分层覆盖率闸门（COVER_ENFORCE 开关，报告先行）+ -race 常开（spec-0.4）
- 集成测试框架：testkit 容器封装（PG/Redis）+ schema 每用例隔离 + ci/integration（spec-0.5）
- 控制面迁移框架：console migrate 子命令 + 0001 RLS 基建（airush_app 角色、tenants 表、
  租户表模板四要素含连接池 GUC 空串加固）+ 编号/不可变 CI 守护（spec-0.6）
- 配置框架：libs/config 声明式加载（聚合校验/secret 脱敏/COMMON 回退）+ pydantic-settings 基类 +
  .env.example 一致性守护（spec-0.7）
- 错误码体系：proto/errors.json SSOT（15 码六级分级）双语言生成 + libs/apierror HTTP 中间件
  （panic 恢复/细节不泄漏）+ 码不可删守护（spec-0.8）
- 可观测性基线：libs/obs 三件套（必带字段/双出口 record 级脱敏/OTLP/label 白名单/降级）+
  gateway healthz/demo 端点 + otel-lgtm 本地栈 + 三信号冒烟脚本（spec-0.9）
- 镜像与部署：三类参数化 Dockerfile（distroless/nonroot/只读 rootfs）+ Helm chart
  （gateway/内置存储/migrate hook）+ make dev-up 一键 kind 全栈（spec-0.10）
- 版本与 release 链路：CHANGELOG 机制、release workflow、release-prep 脚本（spec-0.11）
