FROM node:22-alpine
WORKDIR /app
# Copy the lockfile and install with `npm ci` — the old bare
# `npm install --production` re-resolved the tree on every build
# (non-reproducible) and started dying with npm's arborist
# "Cannot read properties of null (reading 'edgesOut')" bug,
# which broke the Images pipeline for this service.
COPY package.json package-lock.json ./
# The runtime is `node src/index.js`; no package manager is needed after the
# install. Drop the ones node:22-alpine bundles: Trivy flags npm's own
# dependency tree (brace-expansion, picomatch, pacote, sigstore, ip-address
# under /usr/local/lib/node_modules/npm), not this service's lockfile.
RUN npm ci --omit=dev && \
    rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
           /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
           /opt/yarn-v* /usr/local/bin/yarn /usr/local/bin/yarnpkg \
           /root/.npm
COPY src/ src/
RUN adduser -D appuser && chown -R appuser:appuser /app
USER appuser
EXPOSE 8083
# WebSocket-only server; TCP probe is the app-code-free way to verify
# the process is listening. Matches the compose-level healthcheck so
# `docker run` without compose also reports healthy.
HEALTHCHECK --interval=10s --timeout=3s --retries=5 --start-period=10s \
    CMD nc -z localhost 8083 || exit 1
CMD ["node", "src/index.js"]
