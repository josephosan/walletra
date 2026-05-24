FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/bot ./cmd/bot

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/bot /usr/local/bin/bot
WORKDIR /app
CMD ["bot"]
