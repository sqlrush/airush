# spec-0.10 D1：前端静态产物镜像（node 构建 → nginx-unprivileged 运行）。
FROM node:22-slim AS builder
WORKDIR /src
RUN corepack enable 2>/dev/null || npm install -g pnpm@11.21.0
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml frontend/
WORKDIR /src/frontend
RUN pnpm install --frozen-lockfile
COPY frontend/ .
RUN pnpm build

FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --from=builder /src/frontend/dist /usr/share/nginx/html
# nginx-unprivileged 默认 8080 端口、非 root（uid 101）
EXPOSE 8080
