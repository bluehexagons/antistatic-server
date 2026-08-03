FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o antistatic-server .

FROM alpine:latest

RUN apk --no-cache add ca-certificates
RUN addgroup -S antistatic && adduser -S -G antistatic antistatic && mkdir -p /data && chown antistatic:antistatic /data

WORKDIR /app

COPY --from=builder /app/antistatic-server .

USER antistatic
VOLUME ["/data"]

EXPOSE 80 443
EXPOSE 3478/udp

CMD ["./antistatic-server"]
