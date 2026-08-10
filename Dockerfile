# syntax=docker/dockerfile:1

# ---- Stage 1: build the frontend ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Stage 2: build master + agents (frontend embedded into master) ----
FROM golang:1.21-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# embed the freshly built frontend into the master binary
COPY --from=web /web/dist /web/dist
RUN rm -rf master/internal/webassets/dist && cp -r /web/dist master/internal/webassets/dist
# master (pure-Go sqlite, static)
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/nodepanel-master ./master/cmd/master
# agents for every arch served at /dl/
RUN set -eux; \
    for a in amd64 arm64 386; do \
      GOOS=linux GOARCH=$a CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/nodepanel-agent-linux-$a ./agent/cmd/agent; \
    done; \
    GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/nodepanel-agent-linux-arm-7 ./agent/cmd/agent

# ---- Stage 3: runtime ----
FROM alpine:3.20
# git: needed by the GitHub "upload project" integration (master shells out to
# git inside the container to init/commit/push the source tree).
RUN apk add --no-cache ca-certificates tzdata git && adduser -D -H nodepanel
WORKDIR /app
COPY --from=build /out/nodepanel-master /app/nodepanel-master
RUN mkdir -p /app/agents
COPY --from=build /out/nodepanel-agent-linux-* /app/agents/
COPY deploy/docker-entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh /app/nodepanel-master

ENV NODEPANEL_DATA=/var/lib/nodepanel
VOLUME ["/var/lib/nodepanel"]
EXPOSE 8088
ENTRYPOINT ["/app/entrypoint.sh"]
