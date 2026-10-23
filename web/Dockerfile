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

# Pinned by digest, so a moved tag cannot change what ships. Dependabot proposes new digests; each one
# is merged only after the image check in CI has run against it.
FROM nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de AS final
COPY nginx.conf /etc/nginx/nginx.conf
COPY --from=builder /app/security-headers.conf /etc/nginx/security-headers.conf
COPY --from=builder /app/out /usr/share/nginx/html
# Runs as the unprivileged "nginx" user: port 8080, and nginx.conf keeps every path it writes under /tmp.
USER nginx
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:8080/ || exit 1
