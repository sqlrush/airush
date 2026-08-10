# spec-0.10 D1：Python skill 服务镜像（uv 构建 → slim 运行，nonroot）。
# 当前 skills 尚无服务进程（spec-1.9 实装 MCP server），本镜像为骨架：
# 可 import 包并以 python -c 冒烟；ENTRYPOINT 随 spec-1.9 定为 server 启动。
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim AS builder
WORKDIR /src
COPY pyproject.toml uv.lock ./
COPY skills/pyproject.toml skills/
RUN uv sync --frozen --no-dev --no-install-workspace
COPY skills/ skills/
RUN uv sync --frozen --no-dev

FROM python:3.12-slim-bookworm
WORKDIR /app
COPY --from=builder /src/.venv /app/.venv
COPY --from=builder /src/skills /app/skills
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1
USER 65532:65532
ENTRYPOINT ["python", "-c", "import airush_skills; print('airush-skills', airush_skills.__version__)"]
