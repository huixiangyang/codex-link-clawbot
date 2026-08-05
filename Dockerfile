FROM golang:1.25-alpine AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X github.com/huixiangyang/weclaw/cmd.Version=${VERSION}" \
    -o /usr/local/bin/weclaw .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates ffmpeg tzdata
COPY --from=builder /usr/local/bin/weclaw /usr/local/bin/weclaw

VOLUME /root/.weclaw
ENTRYPOINT ["weclaw"]
CMD ["start"]
