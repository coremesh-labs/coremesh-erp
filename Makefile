# CoreMesh ERP – Fachplugins. Binaries nach der Namenskonvention des Resolvers:
# bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
BIN_DIR := bin

.PHONY: build test run tectonic

build: tectonic
	go build -o $(BIN_DIR)/plugins/le/ledger-0.28.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/ledger
	go build -o $(BIN_DIR)/plugins/re/realestate-0.3.9-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/realestate
	go build -o $(BIN_DIR)/plugins/co/contract-0.36.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/contract
	go build -o $(BIN_DIR)/plugins/pr/procurement-0.11.3-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/procurement
	go build -o $(BIN_DIR)/plugins/op/opcost-0.6.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/opcost
	go build -o $(BIN_DIR)/plugins/ba/bank-0.1.9-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/bank
	go build -o $(BIN_DIR)/plugins/dm/dms-0.13.4-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/dms
	go build -o $(BIN_DIR)/plugins/te/template-0.7.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/template

# Tectonic (LaTeX → PDF) zum Einbetten in das Plugin template laden (mit Wiederholungen).
tectonic:
	GOOS=$(GOOS) GOARCH=$(GOARCH) tools/fetch-tectonic.sh $(GOOS) $(GOARCH)

test:
	go vet ./... && go test ./...

# Startet den Host des Kerns mit dessen configs und den configs dieses Repositories.
run: build
	cd ../coremesh && ./bin/host$(EXT) -config configs,../coremesh-erp/configs
