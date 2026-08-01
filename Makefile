.PHONY: all build test clean vet lint sec-scan build-linux deploy deploy-full

BINARY_NAME=hubfly-builder
TEST_SERVER=root@100.66.212.61
DEPLOY_SERVERS ?= root@100.66.212.61 root@100.66.229.114

all: build

build:
	go build -o $(BINARY_NAME) ./cmd/hubfly-builder/main.go

test:
	go test -v ./...

clean:
	go clean
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_NAME)-linux

vet:
	go vet ./...

lint:
	golangci-lint run

sec-scan:
	govulncheck ./...

build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_NAME)-linux ./cmd/hubfly-builder/main.go

deploy:
	DEPLOY_SERVERS="$(DEPLOY_SERVERS)" BINARY_NAME="$(BINARY_NAME)" bash scripts/deploy.sh

deploy-full: deploy
	@echo "==> Creating user and directories..."
	@ssh $(TEST_SERVER) ' \
		id -u hubfly-builder &>/dev/null || useradd --system --shell /usr/sbin/nologin --home-dir /var/lib/hubfly-builder hubfly-builder; \
		mkdir -p /etc/hubfly-builder /var/lib/hubfly-builder /var/log/hubfly-builder /etc/sudoers.d; \
		chown -R hubfly-builder:hubfly-builder /etc/hubfly-builder /var/lib/hubfly-builder /var/log/hubfly-builder \
	'
	@echo "==> Updating systemd and sudoers..."
	@scp packaging/systemd/hubfly-builder.service $(TEST_SERVER):/etc/systemd/system/
	@scp packaging/sudoers/hubfly-builder $(TEST_SERVER):/etc/sudoers.d/
	@ssh $(TEST_SERVER) 'chmod 440 /etc/sudoers.d/hubfly-builder && systemctl daemon-reload && systemctl enable --now $(BINARY_NAME) && systemctl restart $(BINARY_NAME)'
	@echo "==> Full deployment complete!"
