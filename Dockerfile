# Stage 1: build a static binary
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /sentinel ./cmd/sentinel

# Stage 2: minimal runtime — ca-certificates needed for HTTPS/TLS checks
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /sentinel /usr/local/bin/sentinel
COPY configs/sentinel.yaml /etc/sentinel/sentinel.yaml

EXPOSE 8080

# Public mode: every visitor gets their own isolated board. Perfect for
# a demo URL where strangers try the tool. Remove this line for a
# private server where the board is shared and persisted.
ENV SENTINEL_PUBLIC=1

# Render (and most PaaS) inject PORT; fall back to 8080 elsewhere.
CMD ["/bin/sh", "-c", "sentinel serve --config /etc/sentinel/sentinel.yaml --listen :${PORT:-8080}"]
