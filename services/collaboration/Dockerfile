FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install --production
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
