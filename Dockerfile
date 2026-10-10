# Build stage.
FROM golang:1.24 AS build
# Fetch a newer toolchain if go.mod asks for one.
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/pickem ./cmd/pickem \
 && mkdir -p /out/data

# Runtime stage: static binary, no shell, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pickem /pickem
# Owned by the nonroot user so a fresh named volume is writable.
COPY --from=build --chown=65532:65532 /out/data /data
ENV DB_PATH=/data/pickem.db
VOLUME /data
ENTRYPOINT ["/pickem"]
