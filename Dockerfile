# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies change far less often than source, so resolve them in their own
# cached layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/barghvim .

# The binary is static and embeds the IANA timezone database via the
# time/tzdata import, so the runtime image only has to supply root CAs for the
# outbound TLS call to the upstream API.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/barghvim /barghvim

# scratch has no /etc/passwd, so run as a numeric unprivileged uid.
USER 65534:65534

EXPOSE 8080
ENV ADDR=:8080

ENTRYPOINT ["/barghvim"]
