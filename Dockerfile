FROM golang:1.26-alpine@sha256:91eda9776261207ea25fd06b5b7fed8d397dd2c0a283e77f2ab6e91bfa71079d AS builder

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
