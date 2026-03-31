package allowlist

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

type AllowedCommands struct {
	Prebuild []string `json:"prebuild"`
	Build    []string `json:"build"`
	Run      []string `json:"run"`
}

func DefaultAllowedCommands() *AllowedCommands {
	return &AllowedCommands{
		Prebuild: []string{
			"npm install",
			"npm ci",
			"yarn install",
			"yarn install --frozen-lockfile",
			"yarn install --immutable",
			"pnpm install",
			"pnpm install --frozen-lockfile",
			"go work sync",
			"pip install -r requirements.txt",
			"pip install pipenv && pipenv install --system --deploy",
			"pip install .",
			"composer install",
			"composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction",
			"go mod download",
			"bun install",
			"bun install --frozen-lockfile",
			"mvn clean",
			"./mvnw clean",
			"gradle dependencies",
			"./gradlew dependencies",
		},
		Build: []string{
			"npm run build",
			"npm run build:*",
			"npm run generate",
			"npm run generate:*",
			"yarn build",
			"yarn run build",
			"yarn run build:*",
			"yarn build:*",
			"yarn generate",
			"yarn run generate",
			"yarn run generate:*",
			"yarn generate:*",
			"pnpm run build",
			"pnpm run build:*",
			"pnpm build",
			"pnpm build:*",
			"pnpm run generate",
			"pnpm run generate:*",
			"pnpm generate",
			"pnpm generate:*",
			"go build ./...",
			"go build -o app .",
			"go build -o app ./cmd/*",
			"go build -o app ./*",
			"bun build",
			"bun run build",
			"bun run generate",
			"bun run generate:*",
			"python setup.py build",
			"composer dump-autoload --optimize",
			"php artisan optimize",
			"php bin/console cache:clear --env=prod --no-debug",
			"mvn install -DskipTests",
			"./mvnw install -DskipTests",
			"gradle build -x test",
			"./gradlew build -x test",
		},
		Run: []string{
			"npm start",
			"npm run start",
			"npm run *",
			"npm run serve",
			"npm run preview",
			"npm run dev",
			"yarn start",
			"yarn run start",
			"yarn run *",
			"yarn serve",
			"yarn preview",
			"yarn dev",
			"yarn run serve",
			"yarn run preview",
			"yarn run dev",
			"pnpm start",
			"pnpm run start",
			"pnpm run *",
			"pnpm run serve",
			"pnpm run preview",
			"pnpm run dev",
			"pnpm serve",
			"pnpm preview",
			"pnpm dev",
			"go run main.go",
			"go run .",
			"go run ./cmd/*",
			"go run ./*",
			"./app",
			"python main.py",
			"python app.py",
			"python server.py",
			"python run.py",
			"python manage.py",
			"python manage.py runserver 0.0.0.0:${PORT:-8000}",
			"python *.py",
			"python -m *",
			"uvicorn *:* --host 0.0.0.0 --port ${PORT:-8000}",
			"uvicorn *:app --host 0.0.0.0 --port ${PORT:-8000}",
			"uvicorn *:application --host 0.0.0.0 --port ${PORT:-8000}",
			"gunicorn *:* --bind 0.0.0.0:${PORT:-8000}",
			"gunicorn *:app --bind 0.0.0.0:${PORT:-8000}",
			"gunicorn *:application --bind 0.0.0.0:${PORT:-8000}",
			"flask run --host=0.0.0.0 --port=${PORT:-8000}",
			"bun run start",
			"apache2-foreground",
			"php-fpm -D && exec nginx -g 'daemon off;'",
			"php *.php",
			"php artisan queue:work",
			"php bin/console messenger:consume async",
			"node server.js",
			"java -jar target/*.jar",
			"java -jar build/libs/*.jar",
		},
	}
}

func LoadAllowedCommands(path string) (*AllowedCommands, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cmds AllowedCommands
	if err := json.Unmarshal(data, &cmds); err != nil {
		return nil, err
	}

	return &cmds, nil
}

func IsCommandAllowed(cmd string, allowed []string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}

	for _, a := range allowed {
		pattern := strings.TrimSpace(a)
		if pattern == "" {
			continue
		}
		if pattern == cmd {
			return true
		}
		if strings.Contains(pattern, "*") && wildcardMatch(pattern, cmd) {
			return true
		}
	}
	return false
}

func wildcardMatch(pattern, value string) bool {
	parts := strings.Split(pattern, "*")

	var builder strings.Builder
	builder.WriteString("^")
	for i, part := range parts {
		builder.WriteString(regexp.QuoteMeta(part))
		if i < len(parts)-1 {
			// Keep wildcard matching strict: command tokens can only contain safe chars.
			builder.WriteString("[A-Za-z0-9:._/\\-]+")
		}
	}
	builder.WriteString("$")

	matched, err := regexp.MatchString(builder.String(), value)
	return err == nil && matched
}
