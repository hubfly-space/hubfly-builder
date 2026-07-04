# Hubfly Builder

**Hubfly Builder** Is a high-performance, standalone Go service designed to orchestrate container image builds through the Hubcell native build CLI. It provides a robust API for managing build jobs, supports automatic runtime detection, uses a built-in command allowlist for generated commands, and ensures persistence through a local SQLite database.

## Architecture & Features

- **Built with Go:** High-performance, concurrent execution model.
- **Hubcell Native Build Backend:** Runs `sudo hubcell build` directly for image builds.
- **SQLite Persistence:** All job metadata, status, and history are stored locally, allowing the builder to resume operations after restarts.
- **Auto-Detection (Zero-Config):** Automatically detects the runtime (Node.js, Bun, Go, Python, Java, etc.) and generates an optimized Dockerfile if one isn't provided.
- **Secure by Design:** Auto-detected commands are validated against a built-in allowlist.
- **Structured Logging:** Job logs are captured, stored locally, and served via API.
- **Backend Integration:** Reports build outcomes (success/failure) via configurable webhooks.
- **Hubcell Local Images:** Generated image tags use the `hubcell.local` registry expected by Hubcell builds.
- **Resource Management:** Supports configurable per-job resource limits (CPU/Memory).
- **Cleanup Automation:** Automatically prunes build workspaces and implements log retention policies.
- **Security Scanning:** Integrated automated security workflows using Govulncheck and Trivy to monitor for vulnerabilities in Go dependencies and the codebase.

---

## Configuration

### Global Configuration

The production service reads a global JSON config from `/etc/hubfly-builder/config.json`. If the file is missing on startup, the builder creates it with production defaults. Set `HUBFLY_BUILDER_CONFIG` to use a different config path.

For local development, if the global config cannot be created and `HUBFLY_BUILDER_CONFIG` is not set, the builder falls back to `configs/env.json`.

| Key                     | Description                                                  | Default / Example                              |
| :---------------------- | :----------------------------------------------------------- | :--------------------------------------------- |
| `HUBCELL_BASE_URL`      | Hubcell API base URL used by ancillary Hubcell integrations  | `http://127.0.0.1:10012`                       |
| `HUBCELL_CLI_PATH`      | Hubcell executable path, or a directory containing `hubcell` | `/usr/local/bin/hubcell`                       |
| `CALLBACK_URL`          | Backend webhook for reporting results                        | `https://api.hubfly.space/api/builds/callback` |
| `SERVER_ADDR`           | Build API listen address                                     | `:10008`                                       |
| `UPLOAD_ADDR`           | Image upload API listen address                              | `:10011`                                       |
| `DATA_DIR`              | SQLite state directory                                       | `/var/lib/hubfly-builder` under systemd        |
| `LOG_DIR`               | System and job log directory                                 | `/var/log/hubfly-builder` under systemd        |
| `MAX_CONCURRENT_BUILDS` | Concurrent build worker limit                                | `3`                                            |
| `LOG_RETENTION_DAYS`    | Job log retention window                                     | `7`                                            |
| `UPDATE_LOCKFILE`       | Lockfile path to signal active builds                        | `/run/hubfly-builder-update.lock`              |

Example `/etc/hubfly-builder/config.json`:

```json
{
  "HUBCELL_BASE_URL": "http://127.0.0.1:10012",
  "HUBCELL_CLI_PATH": "/usr/local/bin/hubcell",
  "CALLBACK_URL": "https://api.hubfly.space/api/builds/callback",
  "SERVER_ADDR": ":10008",
  "UPLOAD_ADDR": ":10011",
  "DATA_DIR": "/var/lib/hubfly-builder",
  "LOG_DIR": "/var/log/hubfly-builder",
  "MAX_CONCURRENT_BUILDS": 3,
  "LOG_RETENTION_DAYS": 7,
  "UPDATE_LOCKFILE": "/run/hubfly-builder-update.lock"
}
```

Environment variables with the same names override file values.

### Hubcell Build Configuration

Build jobs call `sudo <HUBCELL_CLI_PATH> build` with the job image tag, required build capabilities, requested network, memory bytes, CPU period/quota, and rootfs sizing flags.

---

## Runtime Layout

At runtime the builder creates and uses these local paths:

| Path                                            | Purpose                                          |
| :---------------------------------------------- | :----------------------------------------------- |
| `/etc/hubfly-builder/config.json`               | Global service config                            |
| `/var/lib/hubfly-builder/hubfly-builder.sqlite` | SQLite database for jobs and state under systemd |
| `/var/log/hubfly-builder/`                      | System log and per-job build logs under systemd  |
| `./configs/env.json`                            | Local development fallback config                |
| `./data/`, `./log/`                             | Local development state and logs                 |

The packaged systemd unit creates `/etc/hubfly-builder`, `/var/lib/hubfly-builder`, and `/var/log/hubfly-builder` with ownership assigned to the `hubfly-builder` user.

---

## Supported Runtimes & Auto-Detection

When `isAutoBuild` is set to `true`, the builder inspects the repository root (or the specified `workingDir`) to identify the runtime:

| Runtime     | Detection File                                              | Default Image                                     |
| :---------- | :---------------------------------------------------------- | :------------------------------------------------ |
| **Bun**     | `bun.lock`                                                  | `oven/bun:1.2`                                    |
| **Node.js** | `package.json`                                              | `node:18-alpine`                                  |
| **Go**      | `go.mod`                                                    | `golang:1.18-alpine`                              |
| **Python**  | `requirements.txt`, `pyproject.toml`, `setup.py`, `Pipfile` | `python:3.14.4-slim`                              |
| **Java**    | `pom.xml`, `build.gradle`, `build.gradle.kts`               | `maven:3.9-eclipse-temurin-17` / `gradle:8-jdk17` |
| **Static**  | `index.html`                                                | `nginx:alpine`                                    |
| **PHP**     | `composer.json`                                             | `php:8.3-apache` / `php:8.3-fpm` / `php:8.3-cli`  |

If a `Dockerfile` exists in the context, it takes precedence over auto-detection.

---

## Image Tagging Scheme

Images are tagged according to the following pattern:
`hubcell.local/{USER_ID}/{PROJECT_ID}:{SHORT_COMMIT_SHA}-b{BUILD_ID}-v{TIMESTAMP}`

**Example:**
`hubcell.local/user-123/my-app:abc123456789-b-build-456-v20260210T123000Z`

---

## API Documentation

### 1. Create Build Job

Creates a new build job and queues it for execution.

- **URL:** `/api/v1/jobs`
- **Method:** `POST`
- **Payload:**

```json
{
  "id": "build_uuid_123",
  "projectId": "my-awesome-project",
  "userId": "user_99",
  "sourceType": "git",
  "sourceInfo": {
    "gitRepository": "https://github.com/user/repo.git",
    "ref": "main",
    "commitSha": "optional_full_sha",
    "workingDir": "src"
  },
  "buildConfig": {
    "isAutoBuild": true,
    "runtime": "bun",
    "version": "1.2",
    "prebuildCommand": "bun install",
    "buildCommand": "bun run build",
    "dockerfileArgs": {
      "BUILD_VERSION": "2026.05.01"
    },
    "dockerfileEnv": {
      "APP_ENV": "production"
    },
    "network": "user123_net",
    "env": {
      "NEXT_PUBLIC_API_URL": "https://api.example.com",
      "DATABASE_URL": "postgres://...",
      "SENTRY_AUTH_TOKEN": "..."
    },
    "envOverrides": {
      "NEXT_PUBLIC_API_URL": { "secret": true },
      "DATABASE_URL": { "scope": "build", "secret": true }
    },
    "timeoutSeconds": 3600,
    "resourceLimits": {
      "cpu": 0.5,
      "memoryMB": 2048
    }
  }
}
```

`buildConfig.resourceLimits` is currently accepted for request compatibility but ignored during Hubcell builds. The builder always uses fixed defaults of `cpu=2` and `memoryMB=4096`.

`buildConfig.env` is always treated in `auto` mode:

- Public-prefixed vars (e.g. `NEXT_PUBLIC_`, `VITE_`) are resolved as `both` (build + runtime).
- Keys with build evidence (`Dockerfile ARG`/reference or known build config references) are resolved to `build`.
- Unknown keys default to `runtime`.
- Unknown/sensitive keys default to `secret`; native Hubcell builds currently log a warning because the CLI does not accept secret mounts.
- The resolved result is returned as `buildConfig.resolvedEnvPlan` and callback metadata (`runtimeEnvKeys`).

`buildConfig.envOverrides` is optional:

- If provided for a key, override values take precedence over auto-detection.
- `scope` supports `build`, `runtime`, or `both`.
- `secret` (`true`/`false`) forces whether the key is mounted as a build secret vs passed as build-arg when build scope is active.

`buildConfig.dockerfileArgs` and `buildConfig.dockerfileEnv` are optional and only apply when a `Dockerfile` is found in the repository:

- `dockerfileArgs` are injected as Dockerfile `ARG` declarations.
- `dockerfileEnv` entries are injected as `ARG` + `ENV` declarations.
- These fields are ignored for generated Dockerfiles and for `customDockerfile`.
- Do not put secrets in `dockerfileEnv`; `ENV` values are baked into the resulting image.

`buildConfig.buildContextDir` is optional for repository Dockerfiles:

- By default, the Dockerfile build context is the repository root (`"."`), even when `sourceInfo.workingDir` points to a subdirectory Dockerfile.
- Set it to a narrower ancestor directory when you want a smaller context.
- The context must stay inside the repository and must contain `sourceInfo.workingDir`.
- Use `.dockerignore` to keep a wider context isolated to only the files the Dockerfile needs.

`buildConfig.customDockerfile` is optional:

- Send plain Dockerfile text in this field to force the builder to use that Dockerfile.
- A custom Dockerfile takes precedence over any `Dockerfile` committed in the repository.
- The build context defaults to `sourceInfo.workingDir` when a custom Dockerfile is provided.
- Example: `"customDockerfile": "FROM node:22-alpine\nWORKDIR /app\nCOPY . .\nRUN npm ci\nCMD [\"npm\", \"start\"]\n"`

`buildConfig.network` is required:

- The worker passes this value to `hubcell build --network`.
- Build requests add only `CHOWN`, `FOWNER`, `FSETID`, `SETUID`, and `SETGID`.
- If missing/empty, the job is rejected with `no user network provided`.

### Gateway Port Mapping

- This applies to static sites served by the generated nginx runtime.
- Static nginx listens on port `80` and `8080` by default, and both are exposed in the generated Dockerfile.
- Callback payload includes `exposePort` for static runtime only.

Examples:

- Docker publish: `-p 80:8080`
- Kubernetes Service: `port: 80`, `targetPort: 8080`
- Nginx reverse proxy: `proxy_pass http://app:8080;`

Callback payload excerpt:

```json
{
  "id": "build_uuid_123",
  "status": "success",
  "imageTag": "hubcell.local/user-123/my-app:abc123-bbuild_uuid_123-v20260210T123000Z",
  "exposePort": "8080"
}
```

- **Responses:**
  - `201 Created`: Job successfully queued. The response body includes the fully populated `BuildConfig`, including the auto-generated `dockerfileContent` (if `isAutoBuild` was `true`).
  - `400 Bad Request`: Invalid payload or failed repository inspection.
  - `500 Internal Server Error`: Storage failure.

- **Example:**

```bash
curl -X POST http://localhost:10008/api/v1/jobs \
  -H "Content-Type: application/json" \
  -d '{"id":"b1", "projectId":"p1", "userId":"u1", "sourceType":"git", "sourceInfo":{"gitRepository":"https://github.com/bonheur15/hubfly-sample-react-bun.git"}, "buildConfig":{"isAutoBuild":true,"network":"proj-network-p1"}}'
```

### 2. Get Job Status

Retrieves the full metadata and current status of a job.

- **URL:** `/api/v1/jobs/{id}`
- **Method:** `GET`
- **Responses:**
  - `200 OK`: Returns the `BuildJob` object.
  - `404 Not Found`: `{"error": "JOB_NOT_FOUND", "message": "job not found"}`

- **Example:**

```bash
curl -i http://localhost:10008/api/v1/jobs/b1
```

### 3. Get Job Logs

Returns the raw text logs of the build process.

- **URL:** `/api/v1/jobs/{id}/logs`
- **Method:** `GET`
- **Responses:**
  - `200 OK`: `text/plain` stream of logs.
  - `404 Not Found`: `{"error": "BUILD_LOG_NOT_FOUND", "message": "build log not found"}`

- **Example:**

```bash
curl http://localhost:10008/api/v1/jobs/b1/logs
```

### 4. Health Check

Basic availability check.

- **URL:** `/healthz`
- **Method:** `GET`
- **Response:** `200 OK` ("OK")

---

## Development & Debugging Endpoints

### List Running Builds

Lists all jobs currently in `claimed` or `building` state.

- **URL:** `/dev/running-builds`
- **Method:** `GET`

### Reset Database

Clears all jobs from the SQLite database. **Use with caution.**

- **URL:** `/dev/reset-db`
- **Method:** `POST`

---

## Errors and Status Codes

| Code       | Status | Meaning                                      |
| :--------- | :----- | :------------------------------------------- |
| `pending`  | 201    | Job created, waiting for worker.             |
| `claimed`  | -      | Job picked up by a worker.                   |
| `building` | -      | Hubcell build or Git operations in progress. |
| `success`  | -      | Build completed successfully.                |
| `failed`   | -      | An error occurred during the build process.  |
| `canceled` | -      | Job was manually terminated.                 |

---

## Getting Started

### Linux Prerequisites

The builder is intended to run on Linux with Hubcell available locally. Hubcell performs native Dockerfile builds and stores the resulting image under `hubcell.local`.

Required commands:

- `sudo`
- the Hubcell CLI referenced by `HUBCELL_CLI_PATH`
- `git`

To build the binary from source, you also need a working Go toolchain installed locally.

Recommended baseline packages on Debian/Ubuntu:

```bash
sudo apt-get update
sudo apt-get install -y git curl ca-certificates
```

Before starting the builder, verify the host is ready:

```bash
curl -fsS "$HUBCELL_BASE_URL/v1/cells" >/dev/null
sudo "$HUBCELL_CLI_PATH" build --help
git --version
```

The builder process must be able to:

- talk to the Hubcell API
- run the Hubcell CLI through `sudo`
- clone Git repositories over the network
- write to its configured `DATA_DIR` and `LOG_DIR`

### One-Line Installation (Linux)

You can install or update Hubfly Builder with a single command. This script detects the Linux architecture, downloads the matching GitHub release bundle, verifies its checksum, installs the binary, and sets up the systemd service and sudoers entry. Go is not required on the target machine.

```bash
curl -sSL https://raw.githubusercontent.com/hubfly-space/hubfly-builder/main/scripts/install.sh | sudo bash
```

To install a specific release instead of the latest one:

```bash
curl -sSL https://raw.githubusercontent.com/hubfly-space/hubfly-builder/main/scripts/install.sh | sudo INSTALL_VERSION=v1.8.3 bash
```

### Safe Updates

Hubfly Builder supports safe updates. When the installation script is run, it checks for an active lockfile (`/run/hubfly-builder-update.lock`). If builds are currently running, the installer will wait for them to complete before stopping the service and replacing the binary. This ensures that no builds are interrupted during an update.

### Systemd Install

Create a dedicated service user and install the binary:

```bash
sudo useradd --system --home /var/lib/hubfly-builder --shell /usr/sbin/nologin hubfly-builder
sudo install -m 755 hubfly-builder /usr/local/bin/hubfly-builder
sudo install -m 644 packaging/systemd/hubfly-builder.service /etc/systemd/system/hubfly-builder.service
sudo install -m 440 packaging/sudoers/hubfly-builder /etc/sudoers.d/hubfly-builder
sudo visudo -cf /etc/sudoers.d/hubfly-builder
sudo systemctl daemon-reload
sudo systemctl enable --now hubfly-builder
```

The unit runs as `hubfly-builder:hubfly-builder`, uses `/etc/hubfly-builder/config.json`, and stores state/logs in `/var/lib/hubfly-builder` and `/var/log/hubfly-builder`.

If your Hubcell binary is not `/usr/local/bin/hubcell`, update both:

- `/etc/hubfly-builder/config.json`
- `/etc/sudoers.d/hubfly-builder`

Then restart:

```bash
sudo systemctl restart hubfly-builder
```

### Builder Runtime Notes

For each job, the builder:

- clones the repository into a temporary workspace
- generates or stages the Dockerfile when needed
- generates a `hubcell.local/<user>/<project>:<source>-b<job>-v<timestamp>` image tag
- runs `sudo <HUBCELL_CLI_PATH> build --verbose --cap-add CHOWN --cap-add FOWNER --cap-add FSETID --cap-add SETUID --cap-add SETGID -t <generated-image-tag> --network <request-buildConfig.network> -m <bytes> --cpu-period <period> --cpu-quota <quota> --rootfs-initial-size 10g <dockerfile-directory>`
- records the resulting image tag
- removes the temporary workspace

The Hubcell virtual network named by `buildConfig.network` is passed directly to the Hubcell build CLI.

### Build From Source

```bash
git clone https://github.com/hubfly/hubfly-builder.git
cd hubfly-builder
go mod download
go build -o hubfly-builder ./cmd/hubfly-builder
```

### Release Bundles

The GitHub release publishes per-platform bundles:

- `hubfly-builder_linux_amd64.tar.gz`
- `hubfly-builder_linux_arm64.tar.gz`
- `hubfly-builder_darwin_amd64.tar.gz`
- `hubfly-builder_darwin_arm64.tar.gz`
- `hubfly-builder_windows_amd64.zip`

Each release asset also has a matching `.sha256` checksum file. Extracting a bundle places the `hubfly-builder` binary (or `hubfly-builder.exe` on Windows) at the archive root, alongside `README.md`.

### Run The Server

```bash
./hubfly-builder
```

The server will start on port `10008` by default.
For local runs, use `configs/env.json` or environment variables when `/etc/hubfly-builder/config.json` is not available.

### Development Run

```bash
go run ./cmd/hubfly-builder
```

### Version Output

Release builds inject the version from the Git tag. To print it:

```bash
./hubfly-builder version
```

This command prints only the version string.

### First-Run Checklist

- ensure Hubcell is running and reachable through `HUBCELL_BASE_URL`
- ensure `HUBCELL_CLI_PATH` points to the Hubcell CLI or its containing directory
- ensure the builder user can run the Hubcell CLI through `sudo`
- ensure `CALLBACK_URL` is reachable from the builder host
- ensure the process user can write `DATA_DIR` and `LOG_DIR`

---

## Development & Deployment Make Commands

A `Makefile` is included to streamline local development, testing, and deployments to your test server.

### Local Development Commands

- `make build`: Compiles the binary locally for your current OS and architecture.
- `make build-linux`: Cross-compiles the binary specifically for Linux AMD64 (`hubfly-builder-linux`).
- `make test`: Runs all unit tests.
- `make vet`: Runs `go vet` to analyze the code for potential errors.
- `make lint`: Runs `golangci-lint` (requires golangci-lint to be installed).
- `make sec-scan`: Scans the codebase for vulnerabilities using `govulncheck`.
- `make clean`: Removes the compiled binaries from the local directory.

### Deployment Commands

The `Makefile` makes it extremely easy to push your local uncommitted changes directly to a remote test server (defaulting to `root@test1-hubfly-node`).

> **Note**: These commands assume you have SSH access to `root@test1-hubfly-node`. You can edit the `TEST_SERVER` variable in the `Makefile` if your test server differs.

#### `make deploy-full` (First-Time Install / Config Updates)

Use this command **the first time** you are installing the builder on the test server, or if you modify the systemd service or sudoers configurations.

- Compiles the Linux binary locally.
- Safely waits for active builds to finish, then stops the service.
- Creates the `hubfly-builder` system user and all necessary directories with correct permissions.
- Uploads and registers the `hubfly-builder.service` and `sudoers` configurations.
- Uploads the binary, reloads the systemd daemon, enables, and starts the service.

#### `make deploy` (Routine Code Updates)

Use this command for **all subsequent deployments** when you only need to push a new compiled binary.

- Cross-compiles the Linux binary locally.
- Checks the remote server for the active lockfile (`/run/hubfly-builder-update.lock`). If a build is running, it will automatically pause and wait for it to finish, ensuring no jobs are interrupted.
- Once safe, it stops the service, updates the binary, and restarts it.
- **Safety**: If it detects the systemd service has not been installed yet, it will warn you to run `make deploy-full` instead.

---

## Utility Commands

### Manual Build Test

To test a build manually using the configured Hubcell CLI:

```bash
sudo "$HUBCELL_CLI_PATH" build \
  --verbose \
  --cap-add CHOWN \
  --cap-add FOWNER \
  --cap-add FSETID \
  --cap-add SETUID \
  --cap-add SETGID \
  -t hubcell.local/test-image:latest \
  --network project-network-demo \
  -m 4294967296 \
  --cpu-period 100000 \
  --cpu-quota 200000 \
  --rootfs-initial-size 10g \
  .
```
