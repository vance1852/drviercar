GO ?= go
export GOTOOLCHAIN = local

.PHONY: build vet test race run measure docker clean

build:
	$(GO) build ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./... -count=1

race:
	$(GO) test -race ./... -count=1

run:
	$(GO) run ./cmd/server

docker:
	docker build -t drviercar:local .

clean:
	rm -rf bin data/drviercar.sqlite data/drviercar.sqlite-shm data/drviercar.sqlite-wal
