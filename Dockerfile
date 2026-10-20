# syntax=docker/dockerfile:1
#
# rukie release image: one distroless non-root stage shipping both
# static binaries. Every base is digest-pinned (tag@sha256), never a
# floating tag. Digests resolved 2026-10-21 against the release pins
# (Go 1.27.2, Rust 1.99.1); see the K10 report for the method.
#
# Build from the parent checkout so both repos are in context:
#   docker build -f rukie/Dockerfile /home/alpha/rewrite/git-renewed

ARG GO_IMAGE=golang:1.27.2-bookworm@sha256:55395706e9703db746cc507abfc4eb2aea75918f8a8024e4848e2cb81004f5ad
ARG RUST_IMAGE=rust:1.99.1-bookworm@sha256:5f29c3b4cb8ad8c6f10df979595582dd67dd146ffebc70718e24ff2e5b4a194d
ARG DISTROLESS_IMAGE=gcr.io/distroless/static-debian13:latest@sha256:9e0130bec991f471279b70f1630ada11e46dad439ef2949544f96001225329fb

FROM ${GO_IMAGE} AS go-build
WORKDIR /src/rukie
COPY rukie/go.mod rukie/go.sum ./
RUN go mod download
COPY rukie/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/rukie ./cmd/rukie \
    && mkdir -p /var-lib-rukie

FROM ${RUST_IMAGE} AS rust-build
RUN apt-get update && apt-get install -y --no-install-recommends musl-tools && rm -rf /var/lib/apt/lists/* \
    && rustup target add x86_64-unknown-linux-musl
WORKDIR /src/ruk
COPY ruk/Cargo.toml ruk/Cargo.lock ./
COPY ruk/crates ./crates
RUN cargo build --release --locked -p ruk --target x86_64-unknown-linux-musl \
    && mkdir -p /out && cp target/x86_64-unknown-linux-musl/release/ruk /out/ruk

FROM ${DISTROLESS_IMAGE}
COPY --from=go-build /out/rukie /usr/bin/rukie
COPY --from=rust-build /out/ruk /usr/lib/rukie/ruk
COPY rukie/deploy/config.example.toml /etc/rukie/config.toml
# VOLUME lands root-owned; ship a nonroot-owned seed so the service
# user can create the data-dir layout on first start.
COPY --from=go-build --chown=65532:65532 /var-lib-rukie /var/lib/rukie
USER 65532:65532
ENTRYPOINT ["/usr/bin/rukie", "serve"]
EXPOSE 3000
VOLUME /var/lib/rukie
HEALTHCHECK --interval=10s --timeout=5s --start-period=20s CMD ["/usr/bin/rukie", "admin", "status"]
