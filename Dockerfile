# syntax=docker/dockerfile:1

# --- admin UI ---
FROM node:25-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

# --- backend (same entrypoint as the official release builds;
# pure Go sqlite, no CGO needed) ---
FROM golang:1.27-alpine AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=ui /src/ui/dist ./ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /app/pocketbase ./examples/base

# --- runtime (mirrors ghcr.io/coollabsio/pocketbase so it is a
# drop-in replacement: same paths, port, entrypoint and healthcheck) ---
FROM alpine:latest
RUN apk add --no-cache ca-certificates curl
COPY --from=backend /app/pocketbase /app/pocketbase

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl --fail http://localhost:8080/api/health || exit 1

ENTRYPOINT ["/app/pocketbase", "serve", "--http=0.0.0.0:8080"]
