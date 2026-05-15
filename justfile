# Build and install the skillvendor CLI.

build:
    go build -o bin/skillvendor ./cmd/skillvendor

test:
    go test ./...

vet:
    go vet ./...

# Install to $GOBIN (or $GOPATH/bin).
install:
    go install ./cmd/skillvendor

# Tidy module deps.
tidy:
    go mod tidy

# Run all gates.
gate: vet test
