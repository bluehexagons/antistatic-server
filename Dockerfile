FROM golang:1.27.3-alpine3.24 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o antistatic-server .

FROM alpine:3.24.2

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/antistatic-server .

EXPOSE 80 443
EXPOSE 3478/udp

CMD ["./antistatic-server"]
