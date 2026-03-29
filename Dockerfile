FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy go.mod and download dependencies
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY main.go .
COPY internal/ internal/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o zitadel-init .

FROM alpine:latest
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates
COPY --from=builder /build/zitadel-init /zitadel-init
ENTRYPOINT ["/zitadel-init"]

