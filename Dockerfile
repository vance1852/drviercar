# Build and run the drviercar road-test operations API.
# The Go toolchain and the warmed module cache stay in the final image so the
# service can be rebuilt and tested inside the container without network access.
FROM golang:1.22

WORKDIR /app

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0 \
    DRVIERCAR_ADDR=0.0.0.0:8080 \
    DRVIERCAR_DB_PATH=/app/data/drviercar.sqlite

COPY go.mod go.sum ./
RUN go mod download all

COPY cmd ./cmd
COPY internal ./internal
COPY Makefile README.md .env.example ./

RUN mkdir -p /app/data \
    && go build ./... \
    && go build -o /app/bin/drviercar-server ./cmd/server

EXPOSE 8080

CMD ["/app/bin/drviercar-server"]
