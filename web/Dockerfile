# SeDoc web — static build served by an unprivileged nginx (H-21).
#
# Build from the REPO ROOT (the build scripts read docs/load-tests/ and write
# the persisted-query manifest under services/graphql-gateway/):
#
#   docker build -f web/Dockerfile -t ghcr.io/aieera/sedoc/web:<tag> .
#
# web/Dockerfile.dockerignore keeps node_modules and the rest of the monorepo
# out of the context (BuildKit picks up <Dockerfile>.dockerignore).
#
# The image serves the SPA only. /api, /yjs and /ws are routed to the Kong
# gateway by the Ingress (Helm) or by whatever fronts the stack; nginx here
# never proxies API traffic.

# ---- build ----
FROM node:22.12.0-alpine3.20 AS build
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci --no-audit --no-fund
COPY web/ ./web/
COPY docs/load-tests/ ./docs/load-tests/
# build-graphql-manifest.mjs writes the backend copy of the persisted-query
# manifest here; the directory must exist even though the web image does not
# ship it.
RUN mkdir -p services/graphql-gateway/internal/persisted && \
    cd web && npm run build

# ---- runtime ----
# nginx stable on Alpine 3.24 (1.27.3-alpine was Alpine 3.20 with 38 fixable
# HIGH/CRITICAL openssl/libxml2 findings). `apk upgrade` pulls the security
# fixes Alpine ships between nginx image rebuilds (brotli-libs 1.2.1-r0 when
# 1.30.6 was pinned), so the Trivy gate does not wait on upstream.
FROM nginx:1.30.6-alpine3.24
RUN apk upgrade --no-cache
# Run as uid/gid 1000 (matches the chart's restricted securityContext) with
# every writable path under /tmp, so readOnlyRootFilesystem works with a
# single emptyDir at /tmp.
COPY web/docker/nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/web/dist/ /usr/share/nginx/html/
RUN rm -f /etc/nginx/conf.d/default.conf
USER 1000:1000
EXPOSE 3000
CMD ["nginx", "-g", "daemon off;"]
