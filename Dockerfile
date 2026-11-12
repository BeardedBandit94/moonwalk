# The moonwalk container image: the binary and nothing else.
#
#   docker build -t moonwalk .
#
# The build stage runs on the machine doing the build and compiles for the
# platform the image is for, so an image for another architecture needs no
# emulation.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/moonwalk ./cmd/moonwalk \
    && scripts/third-party-licenses.sh > /out/THIRD_PARTY_LICENSES

FROM scratch
# Certificates, to read states from S3 over TLS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/moonwalk /moonwalk
COPY --from=build /out/THIRD_PARTY_LICENSES /THIRD_PARTY_LICENSES
COPY LICENSE NOTICE /
# Not root: moonwalk only reads what it is given.
USER 65532:65532
EXPOSE 7878
ENTRYPOINT ["/moonwalk"]
