# Build for the container platform; never bake .env into the image.
FROM golang:1.26.8-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/manage .

FROM alpine:3.23
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/manage /app/manage
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER app
EXPOSE 8000
ENTRYPOINT ["/app/manage"]
CMD ["serve"]
