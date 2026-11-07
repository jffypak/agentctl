# agentctl developer targets. The gate is scripts/check.sh (= scripts/doctor.sh).
BIN := bin/agentctl

.PHONY: build test check doctor clean

build:
	go build -o $(BIN) ./cmd/agentctl

test:
	go test -race ./...

check:
	scripts/check.sh

doctor:
	scripts/doctor.sh

clean:
	rm -rf bin
