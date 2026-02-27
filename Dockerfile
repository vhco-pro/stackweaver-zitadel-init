FROM golang:1.25.7-alpine AS builder

WORKDIR /build

# Copy go.mod and download dependencies
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY main.go .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o zitadel-init main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/zitadel-init /zitadel-init
ENTRYPOINT ["/zitadel-init"]

