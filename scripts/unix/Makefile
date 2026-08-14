.PHONY: all windows linux macos-intel macos-mchip clean

ROOT = ../..
BLD = $(ROOT)/build
CMD = $(ROOT)/cmd

all: clean windows linux macos-intel macos-mchip
	@echo "=== Done! Binaries in $(BLD)/ ==="

# --- Platform targets ---
windows: $(BLD)/windows/devspace.exe
linux: $(BLD)/linux/devspace
macos-intel: $(BLD)/macos-intel/devspace
macos-mchip: $(BLD)/macos-mchip/devspace

# --- Windows ---
$(BLD)/windows/devspace.exe:
	@mkdir -p $(BLD)/windows
	GOOS=windows GOARCH=amd64 go build -o $@ $(CMD)/devspace/
	@echo "  -> $@"

# --- Linux ---
$(BLD)/linux/devspace:
	@mkdir -p $(BLD)/linux
	GOOS=linux GOARCH=amd64 go build -o $@ $(CMD)/devspace/
	@echo "  -> $@"

# --- macOS Intel ---
$(BLD)/macos-intel/devspace:
	@mkdir -p $(BLD)/macos-intel
	GOOS=darwin GOARCH=amd64 go build -o $@ $(CMD)/devspace/
	@echo "  -> $@"

# --- macOS M-chip ---
$(BLD)/macos-mchip/devspace:
	@mkdir -p $(BLD)/macos-mchip
	GOOS=darwin GOARCH=arm64 go build -o $@ $(CMD)/devspace/
	@echo "  -> $@"

clean:
	rm -rf $(BLD)
