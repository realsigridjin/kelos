FROM gcr.io/distroless/static:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-session-server-linux-${TARGETARCH} kelos-session-server
USER 65532:65532
ENTRYPOINT ["/kelos-session-server"]
