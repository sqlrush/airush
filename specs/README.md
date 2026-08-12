# Specs 索引

> spec 命名：`spec-<stage>.<序号>-<slug>.md`。结构模板见 `docs/development-roadmap.md` 第 6 节，
> 详细度门槛见 `CLAUDE.md` 规则 2。状态流转：DRAFT → frozen（user approve）→ shipped。

| Spec | 名称 | 状态 | approve 日期 | shipped 日期 |
|---|---|---|---|---|
| [spec-0.1](spec-0.1-monorepo-scaffold.md) | monorepo 脚手架与构建体系 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.2](spec-0.2-lint-and-style.md) | 代码风格与 lint | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.3](spec-0.3-ci-pipeline.md) | CI/CD pipeline | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.4](spec-0.4-unit-test-framework.md) | 单元测试框架与覆盖率门槛 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.5](spec-0.5-integration-test-framework.md) | 集成测试框架 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.6](spec-0.6-db-schema-migration.md) | 控制面 DB schema 与迁移框架 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.7](spec-0.7-config-framework.md) | 配置框架 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.8](spec-0.8-error-codes.md) | 错误码与 API 错误规范 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.9](spec-0.9-observability-baseline.md) | 可观测性基线 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.10](spec-0.10-image-helm-skeleton.md) | 镜像构建与 Helm chart 骨架 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.11](spec-0.11-versioning-release.md) | 版本号与 release 节奏 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-0.12](spec-0.12-stage0-acceptance.md) | Stage 0 验收 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-1.1](spec-1.1-domain-model-api.md) | 控制面领域模型与 API 骨架 | shipped | 2026-08-10 | 2026-08-10 |
| [spec-1.2](spec-1.2-connector-core.md) | Connector 核心（参照模板级） | frozen · 实施完成 | 2026-08-11 | — |
| [spec-1.17](spec-1.17-direct-connect.md) | 直连接入模式（AD-2②） | frozen · 实施完成 | 2026-08-11 | — |
| [spec-1.3](spec-1.3-metrics-collection.md) | openGauss 采集：指标 | frozen · 实施中 | 2026-08-11 | — |
| [spec-1.4](spec-1.4-slowlog-metadata.md) | openGauss 采集：慢日志与元数据 | **DRAFT** | — | — |

完整规划清单见 `docs/development-roadmap.md` 第 2-4 节；spec 文件在进入起草时才创建。
