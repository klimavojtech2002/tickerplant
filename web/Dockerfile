# syntax=docker/dockerfile:1

# The page is fully client-side (next.config.ts: output "export"), so the build
# produces plain static files and the runtime image needs no Node server at all.
FROM node:22-alpine AS builder
WORKDIR /app
# Keep this version equal to "packageManager" in package.json.
RUN corepack enable && corepack prepare pnpm@10.21.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
# NEXT_PUBLIC_EDGE_URL is inlined at build time (a static export has no server to read it from at request
# time) and is also the one origin the Content-Security-Policy lets the page connect to. The default is the
# edge's host-mapped port in docker-compose.yml, so the common case needs no override.
ARG NEXT_PUBLIC_EDGE_URL=http://127.0.0.1:8080
ENV NEXT_PUBLIC_EDGE_URL=${NEXT_PUBLIC_EDGE_URL}
RUN pnpm build \
 && node scripts/csp-hash.mjs out security-headers.conf.template > security-headers.conf

# nginx 1.27.5, pinned by digest (taken from the image on the author's machine, April 2025 build).
# Refresh the pin on purpose, not by tag drift.
FROM nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10 AS final
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=builder /app/security-headers.conf /etc/nginx/security-headers.conf
COPY --from=builder /app/out /usr/share/nginx/html
# Runs as the unprivileged "nginx" user: port 8080, and nginx.conf keeps every path it writes under /tmp.
USER nginx
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:8080/ || exit 1
