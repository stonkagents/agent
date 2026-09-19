.PHONY: all build test proto-gen lint clean check-installer

all: proto-gen build

build:
	go build -o bin/at cmd/cli/main.go
	go build -o bin/sync-daemon cmd/daemon/main.go
	go build -o bin/cs-tracker tracker/cmd/tracker/main.go

proto-gen:
	./scripts/proto-gen.sh

test:
	go test ./... -v -cover
	@./scripts/check-installer-impact.sh || true

check-installer:
	@./scripts/check-installer-impact.sh

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ api/proto/*.pb.go
