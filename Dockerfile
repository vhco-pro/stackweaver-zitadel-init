FROM golang:1.26-alpine@sha256:f23e8b227fb4493eabe03bede4d5a32d04092da71962f1fb79b5f7d1e6c2a17f AS builder

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY main.go .
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -o zitadel-init .

# Runtime stage, distroless eliminates all OS-level CVEs
# Uses nonroot variant for security; docker-compose overrides to host UID for volume writes
FROM gcr.io/distroless/static:nonroot@sha256:963fa6c544fe5ce420f1f54fb88b6fb01479f054c8056d0f74cc2c6000df5240

COPY --from=builder /build/zitadel-init /zitadel-init

ENTRYPOINT ["/zitadel-init"]
