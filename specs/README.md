# Specs 索引

> spec 命名：`spec-<stage>.<序号>-<slug>.md`。结构模板见 `docs/development-roadmap.md` 第 6 节，
> 详细度门槛见 `CLAUDE.md` 规则 2。状态流转：DRAFT → frozen（user approve）→ shipped。

| Spec | 名称 | 状态 | approve 日期 | shipped 日期 |
|---|---|---|---|---|
| [spec-0.1](spec-0.1-monorepo-scaffold.md) | monorepo 脚手架与构建体系 | DRAFT · 已实施（T1-T5 过） | — | — |
| [spec-0.2](spec-0.2-lint-and-style.md) | 代码风格与 lint | DRAFT · 已实施（T1-T7 过） | — | — |
| [spec-0.3](spec-0.3-ci-pipeline.md) | CI/CD pipeline | DRAFT · 已实施（T3 待分支保护决策） | — | — |
| [spec-0.4](spec-0.4-unit-test-framework.md) | 单元测试框架与覆盖率门槛 | DRAFT · 已实施（阻断待 spec-1.1 激活） | — | — |
| [spec-0.5](spec-0.5-integration-test-framework.md) | 集成测试框架 | DRAFT（预批可开工） | — | — |
| [spec-0.6](spec-0.6-db-schema-migration.md) | 控制面 DB schema 与迁移框架 | DRAFT（预批可开工） | — | — |
| [spec-0.7](spec-0.7-config-framework.md) | 配置框架 | DRAFT（预批可开工） | — | — |
| [spec-0.8](spec-0.8-error-codes.md) | 错误码与 API 错误规范 | DRAFT（预批可开工） | — | — |
| [spec-0.9](spec-0.9-observability-baseline.md) | 可观测性基线 | DRAFT（预批可开工） | — | — |
| [spec-0.10](spec-0.10-image-helm-skeleton.md) | 镜像构建与 Helm chart 骨架 | DRAFT（预批可开工） | — | — |
| [spec-0.11](spec-0.11-versioning-release.md) | 版本号与 release 节奏 | DRAFT（预批可开工） | — | — |
| [spec-0.12](spec-0.12-stage0-acceptance.md) | Stage 0 验收 | DRAFT（验收结论须 user 签署） | — | — |

完整规划清单见 `docs/development-roadmap.md` 第 2-4 节；spec 文件在进入起草时才创建。
