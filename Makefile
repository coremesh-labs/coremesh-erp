# CoreMesh ERP – Fachplugins. Binaries nach der Namenskonvention des Resolvers:
# bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
BIN_DIR := bin

.PHONY: build test run

build:
	go build -o $(BIN_DIR)/plugins/le/ledger-0.2.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/ledger

test:
	go vet ./... && go test ./...

# Startet den Host des Kerns mit dessen configs und den configs dieses Repositories.
run: build
	cd ../coremesh && ./bin/host$(EXT) -config configs,../coremesh-erp/configs
