# Hubfly Builder

**Hubfly Builder** Is a high-performance, standalone Go service designed to orchestrate container image builds using [BuildKit](https://github.com/moby/buildkit). It provides a robust API for managing build jobs, supports automatic runtime detection, uses a built-in command allowlist for generated commands, and ensures persistence through a local SQLite database.

## Architecture & Features

- **Built with Go:** High-performance, concurrent execution model.
- **BuildKit Backend:** Leverages the advanced features of BuildKit for efficient and secure image building.
- **SQLite Persistence:** All job metadata, status, and history are stored locally, allowing the builder to resume operations after restarts.
- **Auto-Detection (Zero-Config):** Automatically detects the runtime (Node.js, Bun, Go, Python, Java, etc.) and generates an optimized Dockerfile if one isn't provided.
- **Secure by Design:** Auto-detected commands are validated against a built-in allowlist.
- **Structured Logging:** Job logs are captured, stored locally, and served via API.
- **Backend Integration:** Reports build outcomes (success/failure) via configurable webhooks.
- **Resource Management:** Supports configurable per-job resource limits (CPU/Memory).
- **Cleanup Automation:** Automatically prunes build workspaces and implements log retention policies.

---

## Configuration

### Environment Variables & Optional `configs/env.json`

The builder can be configured via environment variables or an optional JSON override file at `configs/env.json`. If the file is missing, the builder uses built-in defaults and does not generate the file.

| Key | Description | Default / Example |
| :--- | :--- | :--- |
| `BUILDKIT_ADDR` | Default BuildKit address | `docker-container://buildkitd` |
| `BUILDKIT_HOST` | Default BuildKit host | `docker-container://buildkitd` |
| `REGISTRY_URL` | Default registry to push images to | `127.0.0.1:5000` |
| `CALLBACK_URL` | Backend webhook for reporting results | `https://hubfly.space/api/builds/callback` |
| `PORT` | Port for the builder server to listen on | `8781` |

Example optional `configs/env.json`:

```json
{
  "BUILDKIT_ADDR": "docker-container://buildkitd",
  "BUILDKIT_HOST": "docker-container://buildkitd",
  "REGISTRY_URL": "127.0.0.1:5000",
  "CALLBACK_URL": "https://hubfly.space/api/builds/callback"
}
```

---

## Supported Runtimes & Auto-Detection

When `isAutoBuild` is set to `true`, the builder inspects the repository root (or the specified `workingDir`) to identify the runtime:

| Runtime | Detection File | Default Image |
| :--- | :--- | :--- |
| **Bun** | `bun.lock` | `oven/bun:1.2` |
| **Node.js** | `package.json` | `node:18-alpine` |
| **Go** | `go.mod` | `golang:1.18-alpine` |
| **Python** | `requirements.txt`, `pyproject.toml`, `setup.py`, `Pipfile` | `python:3.9-slim` |
| **Java** | `pom.xml`, `build.gradle`, `build.gradle.kts` | `maven:3.9-eclipse-temurin-17` / `gradle:8-jdk17` |
| **Static** | `index.html` | `nginx:alpine` |
| **PHP** | `composer.json` | `php:8.3-apache` / `php:8.3-fpm` / `php:8.3-cli` |

If a `Dockerfile` exists in the context, it takes precedence over auto-detection.

---

## Image Tagging Scheme

Images are tagged according to the following pattern:
`{REGISTRY}/{USER_ID}/{PROJECT_ID}:{SHORT_COMMIT_SHA}-b{BUILD_ID}-v{TIMESTAMP}`

**Example:**
`registry.hubfly.com/user-123/my-app:abc123456789-b-build-456-v20260210T123000Z`

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

`buildConfig.env` is always treated in `auto` mode:
- Public-prefixed vars (e.g. `NEXT_PUBLIC_`, `VITE_`) are resolved as `both` (build + runtime).
- Keys with build evidence (`Dockerfile ARG`/reference or known build config references) are resolved to `build`.
- Unknown keys default to `runtime`.
- Unknown/sensitive keys default to `secret` and are mounted as BuildKit secrets for build-time usage.
- The resolved result is returned as `buildConfig.resolvedEnvPlan` and callback metadata (`runtimeEnvKeys`).

`buildConfig.envOverrides` is optional:
- If provided for a key, override values take precedence over auto-detection.
- `scope` supports `build`, `runtime`, or `both`.
- `secret` (`true`/`false`) forces whether the key is mounted as a build secret vs passed as build-arg when build scope is active.

`buildConfig.network` is required:
- The worker starts an ephemeral `buildkitd` container for every job on the requested Docker network and uses that same network for builder-to-daemon communication.
- The ephemeral daemon runs OCI workers in `host` network mode and build requests force `network=host`, so build `RUN` containers share the daemon network namespace (including the attached user network).
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
  "imageTag": "registry.hubfly.com/user-123/my-app:abc123-bbuild_uuid_123-v20260210T123000Z",
  "exposePort": "8080"
}
```

- **Responses:**
  - `201 Created`: Job successfully queued. The response body includes the fully populated `BuildConfig`, including the auto-generated `dockerfileContent` (if `isAutoBuild` was `true`).
  - `400 Bad Request`: Invalid payload or failed repository inspection.
  - `500 Internal Server Error`: Storage failure.

- **Example:**
```bash
curl -X POST http://localhost:8781/api/v1/jobs \
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
curl -i http://localhost:8781/api/v1/jobs/b1
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
curl http://localhost:8781/api/v1/jobs/b1/logs
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

| Code | Status | Meaning |
| :--- | :--- | :--- |
| `pending` | 201 | Job created, waiting for worker. |
| `claimed` | - | Job picked up by a worker. |
| `building` | - | BuildKit or Git operations in progress. |
| `success` | - | Build and push completed successfully. |
| `failed` | - | An error occurred during the build process. |
| `canceled` | - | Job was manually terminated. |

---

## Getting Started

### Prerequisites
- **Go 1.18+**
- **Docker CLI + daemon access:** Required for ephemeral per-job BuildKit mode.
- **Git:** Installed and available in PATH.

### Installation
```bash
git clone https://github.com/hubfly/hubfly-builder.git
cd hubfly-builder
go mod download
```

### Running the Server
```bash
go run cmd/hubfly-builder/main.go
```

The server will start on port `8781` by default.



---



## Utility Commands



### Checking Local Registry

If you are running a local registry, you can list repositories and tags using:



```bash

# List all repositories

curl -s http://localhost:5000/v2/_catalog | jq



# List tags for a specific image

curl -s http://localhost:5000/v2/user-123/my-awesome-project/tags/list | jq

```



### Inspecting BuildKit

To see the current BuildKit status for a running ephemeral daemon (use the `addr=` value from job logs):



```bash

buildctl --addr tcp://<ephemeral-buildkit-ip>:1234 debug workers

```



### Manual Build Test

To test a build manually using `buildctl` against an ephemeral daemon:



```bash

buildctl --addr tcp://<ephemeral-buildkit-ip>:1234 build \

  --frontend=dockerfile.v0 \

  --local context=. \

  --local dockerfile=. \

  --output type=image,name=localhost:5000/test-image:latest,push=true

```
