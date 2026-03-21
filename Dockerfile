FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /cub-vmcluster .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /cub-vmcluster /usr/local/bin/cub-vmcluster
ENTRYPOINT ["/usr/local/bin/cub-vmcluster"]
