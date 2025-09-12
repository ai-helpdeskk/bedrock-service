FROM golang:1.21-alpine AS builder

RUN apk add --no-cache git ca-certificates
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o bedrock-service .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
RUN addgroup -g 1000 -S appuser && adduser -u 1000 -S appuser -G appuser

WORKDIR /app
COPY --from=builder /app/bedrock-service .
RUN chown -R appuser:appuser /app

USER appuser
EXPOSE 9000

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9000/health || exit 1

CMD ["./bedrock-service"]
