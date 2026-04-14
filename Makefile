VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)
BINARY  := cc-otel
BIN_DIR := bin
EXT     :=
ifeq ($(OS),Windows_NT)
EXT := .exe
endif
OUT     := $(BIN_DIR)/$(BINARY)$(EXT)

.PHONY: build test coverage clean install

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/cc-otel/

test:
	go test ./... -v

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

clean:
	rm -f $(OUT) $(OUT).exe coverage.out coverage.html

install: build
	./$(OUT) install

vet:
	go vet ./...

lint: vet
	@echo "lint done"
