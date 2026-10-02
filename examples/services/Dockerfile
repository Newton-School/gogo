# syntax=docker/dockerfile:1
FROM golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-p=1
COPY go.mod go.sum gogo.go ./
COPY core ./core
COPY internal ./internal
COPY async ./async
COPY connectors ./connectors
COPY examples/services ./examples/services
RUN go work init . ./async ./async/redis ./connectors/postgres ./connectors/redis ./examples/services
WORKDIR /src/examples/services
ARG SERVICE=api
RUN case "$SERVICE" in \
      manage) target=. ;; \
      api|reports|worker) target=./services/$SERVICE ;; \
      *) exit 2 ;; \
    esac && go test ./... && go build -trimpath -ldflags="-s -w" -o /out/service "$target"

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS runtime
RUN addgroup -g 10001 gogo && adduser -D -u 10001 -G gogo gogo
WORKDIR /app
COPY --from=build /out/service /app/service
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY LICENSE /app/LICENSE
USER 10001:10001
ENTRYPOINT ["/app/service"]
CMD ["serve"]
