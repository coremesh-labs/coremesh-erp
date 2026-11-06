# CoreMesh ERP – Fachplugins. Binaries nach der Namenskonvention des Resolvers:
# bin/plugins/<xx>/<name>-<version>-<os>-<arch>[.exe]
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
EXT     := $(if $(filter windows,$(GOOS)),.exe,)
BIN_DIR := bin

.PHONY: build test run tectonic

build: tectonic
	go build -o $(BIN_DIR)/plugins/le/ledger-0.39.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/ledger
	go build -o $(BIN_DIR)/plugins/re/realestate-0.5.4-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/realestate
	go build -o $(BIN_DIR)/plugins/as/asset-0.3.3-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/asset
	go build -o $(BIN_DIR)/plugins/te/techobj-0.7.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/techobj
	go build -o $(BIN_DIR)/plugins/he/heatcost-0.2.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/heatcost
	go build -o $(BIN_DIR)/plugins/in/inspection-0.1.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/inspection
	go build -o $(BIN_DIR)/plugins/co/contract-0.53.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/contract
	go build -o $(BIN_DIR)/plugins/pr/procurement-0.23.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/procurement
	go build -o $(BIN_DIR)/plugins/op/opcost-0.7.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/opcost
	go build -o $(BIN_DIR)/plugins/ba/bank-0.10.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/bank
	go build -o $(BIN_DIR)/plugins/dm/dms-0.36.0-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/dms
	go build -o $(BIN_DIR)/plugins/te/template-0.17.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/template
	go build -o $(BIN_DIR)/plugins/pr/process-0.5.1-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/process
	go build -o $(BIN_DIR)/plugins/ex/export-0.3.2-$(GOOS)-$(GOARCH)$(EXT) ./cmd/plugins/export

# Tectonic (LaTeX → PDF) zum Einbetten in das Plugin template laden (mit Wiederholungen).
tectonic:
	GOOS=$(GOOS) GOARCH=$(GOARCH) tools/fetch-tectonic.sh $(GOOS) $(GOARCH)

test:
	go vet ./... && go test ./...

# Startet den Host des Kerns mit dessen configs und den configs dieses Repositories.
run: build
	cd ../coremesh && ./bin/host$(EXT) -config configs,../coremesh-erp/configs
