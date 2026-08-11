FROM golang:1.26.5-alpine3.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o antistatic-server .

FROM alpine:3.24

RUN apk --no-cache add ca-certificates
RUN addgroup -S antistatic && adduser -S -G antistatic antistatic && mkdir -p /data && chown antistatic:antistatic /data

WORKDIR /app

COPY --from=builder /app/antistatic-server .

USER antistatic
VOLUME ["/data"]

EXPOSE 80 443
EXPOSE 3478/udp

CMD ["./antistatic-server"]
