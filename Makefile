# Build / verification tasks for sirenhead-tool (Go).
.PHONY: all build test vet fmt check clean install

BIN := sirenhead-tool
PKG := ./cmd/$(BIN)

all: check build

build:
	go build -o $(BIN) $(PKG)

install:
	go install $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

check: fmt vet test
	@test -z "$$(gofmt -l . | grep -v '^$$')" || { echo "gofmt: files need formatting"; gofmt -l .; exit 1; }

# Full verification: formatting, vet, tests, build, and a real-artifact
# round-trip against a Ren'Py game when one is available.
verify:
	./scripts/verify.sh

clean:
	rm -f $(BIN)
