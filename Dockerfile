FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /cub-vmcluster .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /cub-vmcluster /usr/local/bin/cub-vmcluster
ENTRYPOINT ["/usr/local/bin/cub-vmcluster"]
