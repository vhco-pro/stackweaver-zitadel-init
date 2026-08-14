FROM golang:1.26-alpine@sha256:70b46548e42db77e0966aaf3619fd068734dc6c77584d526b91126504fd95816 AS builder

WORKDIR /build

COPY go.mod go.sum* ./
RUN go mod download

COPY main.go .
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux go build -o zitadel-init .

# Runtime stage, distroless eliminates all OS-level CVEs
# Uses nonroot variant for security; docker-compose overrides to host UID for volume writes
FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

COPY --from=builder /build/zitadel-init /zitadel-init

ENTRYPOINT ["/zitadel-init"]
