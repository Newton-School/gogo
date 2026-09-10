# syntax=docker/dockerfile:1

# Build only this public-module consumer, never the framework workspace.
FROM golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
WORKDIR /src
ENV GOWORK=off GOTOOLCHAIN=local GOFLAGS=-p=1 CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go test ./... && go vet ./... \
    && go run manage.go generate --check \
    && go run manage.go makemigrations catalog --check \
    && go run manage.go makemigrations fieldlab --check \
    && go build -trimpath -ldflags="-s -w" -o /out/manage .

# No source, Go toolchain, .env, or build-time credentials in the runtime image.
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS runtime
RUN addgroup -g 10001 showcase && adduser -D -u 10001 -G showcase showcase
WORKDIR /app
COPY --from=build /out/manage /app/manage
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=0555 docker/entrypoint.sh docker/setup.sh /app/docker/
COPY LICENSE /app/LICENSE
USER 10001:10001
EXPOSE 8000
ENTRYPOINT ["/app/docker/entrypoint.sh"]
CMD ["runserver"]
