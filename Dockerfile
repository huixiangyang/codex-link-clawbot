FROM golang:1.25-alpine AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/huixiangyang/codex-link-clawbot/internal/cli.Version=${VERSION}" \
    -o /usr/local/bin/codex-link-clawbot ./cmd/codex-link-clawbot

FROM alpine:3.21

RUN apk add --no-cache ca-certificates ffmpeg tzdata
COPY --from=builder /usr/local/bin/codex-link-clawbot /usr/local/bin/codex-link-clawbot

VOLUME /root/.codex-link-clawbot
ENTRYPOINT ["codex-link-clawbot"]
CMD ["start"]
