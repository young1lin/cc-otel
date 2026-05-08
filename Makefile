BINARY  := cc-otel
BIN_DIR := bin
EXT     :=
ifeq ($(OS),Windows_NT)
EXT := .exe
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
else
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
endif
LDFLAGS := -s -w -X main.version=$(VERSION)
OUT     := $(BIN_DIR)/$(BINARY)$(EXT)

# Detect shell: POSIX (sh/bash) vs CMD
# GNU Make always invokes $(SHELL) which is typically /bin/sh or bash.
# When called from PowerShell/CMD via Git's make, the shell is still bash.
ifeq ($(findstring cmd.exe,$(SHELL)),cmd.exe)
MKDIR_BIN := if not exist "$(BIN_DIR)" mkdir "$(BIN_DIR)"
RM_OUT    := if exist "$(OUT)" del /f /q "$(OUT)"
RM_EXTRA  := if exist "$(OUT).exe" del /f /q "$(OUT).exe"
RM_COV    := if exist coverage.out del /f /q coverage.out
RM_HTML   := if exist coverage.html del /f /q coverage.html
else
MKDIR_BIN := mkdir -p $(BIN_DIR)
RM_OUT    := rm -f $(OUT)
RM_EXTRA  := rm -f $(OUT).exe
RM_COV    := rm -f coverage.out
RM_HTML   := rm -f coverage.html
endif

.PHONY: build test coverage clean install

build:
	$(MKDIR_BIN)
	go build -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/cc-otel/

test:
	go test ./... -v

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

clean:
	$(RM_OUT)
	$(RM_EXTRA)
	$(RM_COV)
	$(RM_HTML)

install: build
	./$(OUT) install

vet:
	go vet ./...

lint: vet
	@echo "lint done"
