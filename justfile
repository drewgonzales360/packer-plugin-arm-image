binary := "packer-plugin-arm-image"
goreleaser := "go tool goreleaser"

# Build the plugin binary using goreleaser (single target, current platform)
build:
    go build -o {{binary}} .

# Build the plugin binary using goreleaser (single target, current platform)
build-release:
    API_VERSION="$(go run . describe 2>/dev/null | jq -r .api_version)" \
        {{goreleaser}} build --snapshot --single-target --clean --output {{binary}}

# Install the plugin locally via packer plugins install
install-local: build
    packer plugins install --path {{binary}} github.com/drewgonzales360/arm-image

# Run unit tests
test count="1":
    go test -race -count {{count}} ./... -timeout=3m

# Run acceptance tests (requires PACKER_ACC=1)
testacc count="1":
    PACKER_ACC=1 go test -count {{count}} -v ./... -timeout=120m

# Run acceptance tests with sudo
testacc-sudo:
    cd pkg/builder && \
    go test -c . && \
    PACKER_ACC=1 PACKER_CONFIG_DIR=$HOME sudo -E bash -c "PATH=$HOME/go/bin:$PATH ./builder.test" && \
    rm -f img.delete builder.test

# Validate generated files are up to date
check-generated:
    ./tools/check_generated.sh

# Generate and zip plugin docs
ci-release-docs:
    rm -rf ./docs
    go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc renderdocs -src docs-src -partials docs-partials/ -dst docs/
    /bin/sh -c "[ -d docs ] && zip -r docs.zip docs/"

# Validate plugin compatibility with packer-sdc
plugin-check: build
    go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc plugin-check {{binary}}

# Create a local snapshot release (no publish)
release-snapshot: check-generated
    API_VERSION="$(go run . describe 2>/dev/null | jq -r .api_version)" \
        {{goreleaser}} release --snapshot --clean --skip=publish

# Create a full release
release: check-generated
    API_VERSION="$(go run . describe 2>/dev/null | jq -r .api_version)" \
        {{goreleaser}} release
