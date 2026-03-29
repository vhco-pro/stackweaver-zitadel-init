FROM golang:1.26-alpine AS builder

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY main.go .
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -o zitadel-init .

# Runtime stage — distroless eliminates all OS-level CVEs
# Includes ca-certificates and tzdata, runs as nonroot (UID 65534) by default
FROM gcr.io/distroless/static@sha256:47b2d72ff90843eb8a768b5c2f89b40741843b639d065b9b937b07cd59b479c6

COPY --from=builder /build/zitadel-init /zitadel-init

ENTRYPOINT ["/zitadel-init"]
