# CoreMesh ERP – Fachplugins. Binaries nach der Namenskonvention des Resolvers:
# bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
BIN_DIR := bin

.PHONY: build test run

build:
	go build -o $(BIN_DIR)/plugins/le/ledger-0.15.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/ledger
	go build -o $(BIN_DIR)/plugins/re/realestate-0.3.2-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/realestate
	go build -o $(BIN_DIR)/plugins/co/contract-0.10.2-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/contract
	go build -o $(BIN_DIR)/plugins/pr/procurement-0.4.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/procurement
	go build -o $(BIN_DIR)/plugins/op/opcost-0.3.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/opcost
	go build -o $(BIN_DIR)/plugins/ba/bank-0.1.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/bank

test:
	go vet ./... && go test ./...

# Startet den Host des Kerns mit dessen configs und den configs dieses Repositories.
run: build
	cd ../coremesh && ./bin/host$(EXT) -config configs,../coremesh-erp/configs
