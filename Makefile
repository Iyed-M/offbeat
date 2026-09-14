BINARY_CLI  := offbeat
BINARY_DAEMON := offbeatd
BUILD_DIR   := bin
LDFLAGS     := -s -w

.PHONY: all build build-cli build-daemon test test-race cover lint vet fmt tidy clean run-cli run-daemon help

all: build

help:
	@echo "Targets:"
	@echo "  build         Build all binaries into $(BUILD_DIR)/"
	@echo "  build-cli     Build the CLI binary (offbeat)"
	@echo "  build-daemon  Build the daemon binary (offbeatd)"
	@echo "  test          Run go test ./..."
	@echo "  test-race     Run go test -race ./..."
	@echo "  cover         Run tests with coverage"
	@echo "  vet           Run go vet ./..."
	@echo "  fmt           Format Go sources"
	@echo "  tidy          Run go mod tidy"
	@echo "  run-cli       Build and run the CLI"
	@echo "  run-daemon    Build and run the daemon"
	@echo "  clean         Remove build artifacts"

build: build-cli build-daemon

build-cli:
	@mkdir -p $(BUILD_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BINARY_CLI) ./cmd/$(BINARY_CLI)

build-daemon:
	@mkdir -p $(BUILD_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BINARY_DAEMON) ./cmd/$(BINARY_DAEMON)

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -cover ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

run-cli: build-cli
	./$(BUILD_DIR)/$(BINARY_CLI) $(ARGS)

run-daemon: build-daemon
	./$(BUILD_DIR)/$(BINARY_DAEMON) $(ARGS)

clean:
	rm -rf $(BUILD_DIR)