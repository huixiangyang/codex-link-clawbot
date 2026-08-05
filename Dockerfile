FROM golang:1.25-alpine AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/huixiangyang/weclaw/cmd.Version=${VERSION}" \
    -o /usr/local/bin/weclaw . && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /usr/local/bin/weclaw-silk-encoder ./cmd/weclaw-silk-encoder

FROM alpine:3.21

RUN apk add --no-cache ca-certificates ffmpeg tzdata
COPY --from=builder /usr/local/bin/weclaw /usr/local/bin/weclaw
COPY --from=builder /usr/local/bin/weclaw-silk-encoder /usr/local/bin/weclaw-silk-encoder
COPY THIRD_PARTY_NOTICES.md /usr/share/doc/weclaw/THIRD_PARTY_NOTICES.md

VOLUME /root/.weclaw
ENTRYPOINT ["weclaw"]
CMD ["start"]
