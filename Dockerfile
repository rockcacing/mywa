# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mywa ./cmd/mywa \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mywa /mywa
COPY --from=build --chown=65532:65532 /out/data /data
ENV MYWA_ADDR=:8080 \
    MYWA_DB_PATH=/data/mywa.db
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/mywa"]
