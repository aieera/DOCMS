FROM node:22-alpine
WORKDIR /app
COPY package.json ./
RUN npm install --production
COPY src/ src/
RUN adduser -D appuser && chown -R appuser:appuser /app
USER appuser
EXPOSE 8083
CMD ["node", "src/index.js"]
