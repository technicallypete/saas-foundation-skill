# syntax=docker/dockerfile:1
# Web (Next.js) image. Build context: web/. Targets: check, runtime.
FROM node:24-slim AS deps
ENV NEXT_TELEMETRY_DISABLED=1
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund

FROM deps AS source
COPY . .

FROM source AS check
# Lint and typecheck; runs without network.
CMD ["npm", "run", "check"]

FROM source AS build
RUN npm run build

FROM node:24-slim AS runtime
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
WORKDIR /app
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
USER node
EXPOSE 3000
CMD ["node", "server.js"]
