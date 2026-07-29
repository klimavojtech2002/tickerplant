# syntax=docker/dockerfile:1

# The page is fully client-side (next.config.ts: output "export"), so the build
# produces plain static files and the runtime image needs no Node server at all.
FROM node:22-alpine AS builder
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@10.21.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
# NEXT_PUBLIC_EDGE_URL is inlined at build time (static export has no server to read
# it from at request time). The default in app/page.tsx already matches the edge's
# host-mapped port in docker-compose.yml, so the common case needs no override.
RUN pnpm build

FROM nginx:1.27-alpine AS final
COPY --from=builder /app/out /usr/share/nginx/html
EXPOSE 80
