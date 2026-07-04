.PHONY: all build test clean vet lint sec-scan build-linux deploy deploy-full

BINARY_NAME=hubfly-builder
TEST_SERVER=root@100.66.212.61

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

deploy: build-linux
	@echo "==> Deploying to $(TEST_SERVER)..."
	@echo "==> Checking for active builds to ensure safe update..."
	@ssh $(TEST_SERVER) ' \
		if systemctl is-active --quiet $(BINARY_NAME); then \
			while [ -f /run/hubfly-builder-update.lock ]; do \
				echo "    Active build detected. Waiting 10s..."; \
				sleep 10; \
			done; \
			echo "    Stopping $(BINARY_NAME) service..."; \
			systemctl stop $(BINARY_NAME); \
		fi \
	'
	@echo "==> Uploading new binary..."
	@scp $(BINARY_NAME)-linux $(TEST_SERVER):/usr/local/bin/$(BINARY_NAME)
	@echo "==> Starting service..."
	@ssh $(TEST_SERVER) 'chmod +x /usr/local/bin/$(BINARY_NAME) && \
		if systemctl list-unit-files | grep -q $(BINARY_NAME).service; then \
			systemctl start $(BINARY_NAME); \
		else \
			echo "    WARNING: Service unit not found. This is expected if it is a first-time install."; \
		fi'
	@rm -f $(BINARY_NAME)-linux
	@echo "==> Deployment complete!"

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
