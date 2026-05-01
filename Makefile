#-----------------------------------------------------------------------------#
#--- Helpers
#-----------------------------------------------------------------------------#

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

#-----------------------------------------------------------------------------#
#--- Development
#-----------------------------------------------------------------------------#

## build: build the cameradashboard binary
.PHONY: build
build:
	@echo "Building cameradashboard..."
	go build -ldflags="-s" -o=./bin/cameradashboard .

## run: build and run the application
.PHONY: run
run: build
	./bin/cameradashboard

## dev: run with hot-reload on file changes (requires air)
.PHONY: dev
dev:
	go run github.com/air-verse/air@latest \
		-build.bin "./bin/cameradashboard" \
		-build.cmd "make build" \
		-build.delay "1000" \
		-build.exclude_dir "bin,tmp,vendor,.git" \
		-build.include_ext "go,html,json,css,js" \
		-build.stop_on_error "true" \
		-build.send_interrupt "true" \
		-misc.clean_on_exit "true"

#-----------------------------------------------------------------------------#
#--- Quality Control
#-----------------------------------------------------------------------------#

## tidy: tidy modfiles and format .go files
.PHONY: tidy
tidy:
	@echo "Tidying modules and formatting code..."
	go mod tidy -v
	go fmt ./...

## test: run all tests
.PHONY: test
test:
	@echo "Running tests..."
	go test -v -race -buildvcs ./...

## test-cover: run all tests and display coverage
.PHONY: test-cover
test-cover:
	@echo "Running tests with coverage..."
	go test -v -race -buildvcs -coverprofile=/tmp/coverage.out ./...
	go tool cover -html=/tmp/coverage.out

## audit: run quality control checks
.PHONY: audit
audit:
	@echo "Running quality control checks..."
	go mod tidy -diff
	go mod verify
	@echo "Checking formatting..."
	test -z "$(shell gofmt -l .)"
	@echo "Running vet..."
	go vet ./...
	@echo "Running static analysis..."
	go run honnef.co/go/tools/cmd/staticcheck@latest -checks=all ./...
	@echo "Running vulnerability checks..."
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## update-deps: update all dependencies
.PHONY: update-deps
update-deps: confirm
	@echo "Updating all dependencies..."
	go get -u -t ./...
	go mod tidy

#-----------------------------------------------------------------------------#
#--- Staging (10.0.0.10)
#-----------------------------------------------------------------------------#

staging_host = '10.0.0.10'
staging_user = 'cms'
staging_dir  = '/opt/cameradashboard'

## staging-build: cross-compile for staging (linux/amd64)
.PHONY: staging-build
staging-build:
	@echo "Building for staging (linux/amd64)..."
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o=./bin/cameradashboard .

## staging-connect: connect to the staging server
.PHONY: staging-connect
staging-connect:
	ssh ${staging_user}@${staging_host}

## staging-deploy: build and deploy binary + assets to staging
.PHONY: staging-deploy
staging-deploy: staging-build
	@echo "Deploying to staging..."
	rsync -rP --delete ./bin/cameradashboard ./templates ./static ./VERSION ${staging_user}@${staging_host}:${staging_dir}/
	ssh ${staging_user}@${staging_host} 'sudo systemctl restart cameradashboard'
	@echo ""
	@echo "Deploy complete:"
	@ssh ${staging_user}@${staging_host} 'sudo systemctl status cameradashboard --no-pager'

## staging-deploy-config: deploy config files to staging
.PHONY: staging-deploy-config
staging-deploy-config:
	@echo "Deploying config files to staging..."
	rsync -P ../configs/mssql_config.json ${staging_user}@${staging_host}:${staging_dir}/config/
	rsync -P ../configs/camera_config.json ${staging_user}@${staging_host}:${staging_dir}/config/
	rsync -P ../configs/camera_adlogin.json ${staging_user}@${staging_host}:${staging_dir}/config/
	@echo "Config files deployed."

## staging-logs: view staging logs (follows)
.PHONY: staging-logs
staging-logs:
	ssh -t ${staging_user}@${staging_host} 'sudo journalctl --unit=cameradashboard --since="24 hours ago" --follow'

## staging-status: check staging service status
.PHONY: staging-status
staging-status:
	@ssh ${staging_user}@${staging_host} 'sudo systemctl status cameradashboard --no-pager'

## staging-stop: stop staging service
.PHONY: staging-stop
staging-stop:
	ssh ${staging_user}@${staging_host} 'sudo systemctl stop cameradashboard'
	@echo "cameradashboard stopped"

## staging-start: start staging service
.PHONY: staging-start
staging-start:
	ssh ${staging_user}@${staging_host} 'sudo systemctl start cameradashboard'
	@echo "cameradashboard started"

## staging-restart: restart staging service
.PHONY: staging-restart
staging-restart:
	ssh ${staging_user}@${staging_host} 'sudo systemctl restart cameradashboard'
	@echo "cameradashboard restarted"

#-----------------------------------------------------------------------------#
#--- Production (10.0.0.11 / app-01)
#-----------------------------------------------------------------------------#

prod_host = 'app-01'
prod_user = 'cms'
prod_dir  = '/opt/cameradashboard'

## prod-build: cross-compile for production (linux/amd64)
.PHONY: prod-build
prod-build:
	@echo "Building for production (linux/amd64)..."
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o=./bin/cameradashboard .

## prod-connect: connect to the production server
.PHONY: prod-connect
prod-connect:
	ssh ${prod_user}@${prod_host}

## prod-deploy: build and deploy binary + assets to production
.PHONY: prod-deploy
prod-deploy: confirm prod-build prod-check-deps
	@echo "Deploying to production..."
	rsync -rP --delete ./bin/cameradashboard ./templates ./static ./VERSION ${prod_user}@${prod_host}:${prod_dir}/
	ssh ${prod_user}@${prod_host} 'sudo systemctl restart cameradashboard'
	@echo ""
	@echo "Deploy complete:"
	@ssh ${prod_user}@${prod_host} 'sudo systemctl status cameradashboard --no-pager'

## prod-deploy-config: deploy config files to production
.PHONY: prod-deploy-config
prod-deploy-config:
	@echo "Deploying config files to production..."
	rsync -P ../configs/mssql_config.json ${prod_user}@${prod_host}:${prod_dir}/config/
	rsync -P ../configs/camera_config.json ${prod_user}@${prod_host}:${prod_dir}/config/
	rsync -P ../configs/camera_adlogin.json ${prod_user}@${prod_host}:${prod_dir}/config/
	@echo "Config files deployed."

## prod-install-service: install the systemd service file (first-time setup)
.PHONY: prod-install-service
prod-install-service:
	@echo "Installing cameradashboard.service on production..."
	rsync -P ./cameradashboard.service ${prod_user}@${prod_host}:/tmp/cameradashboard.service
	ssh ${prod_user}@${prod_host} 'sudo mv /tmp/cameradashboard.service /etc/systemd/system/cameradashboard.service && sudo systemctl daemon-reload && sudo systemctl enable cameradashboard'
	@echo "Service installed and enabled."

## prod-logs: view production logs (follows)
.PHONY: prod-logs
prod-logs:
	ssh -t ${prod_user}@${prod_host} 'sudo journalctl --unit=cameradashboard --since="24 hours ago" --follow'

## prod-status: check production service status
.PHONY: prod-status
prod-status:
	@ssh ${prod_user}@${prod_host} 'sudo systemctl status cameradashboard --no-pager'

## prod-stop: stop production service
.PHONY: prod-stop
prod-stop:
	ssh ${prod_user}@${prod_host} 'sudo systemctl stop cameradashboard'
	@echo "cameradashboard stopped"

## prod-start: start production service
.PHONY: prod-start
prod-start:
	ssh ${prod_user}@${prod_host} 'sudo systemctl start cameradashboard'
	@echo "cameradashboard started"

## prod-restart: restart production service
.PHONY: prod-restart
prod-restart:
	ssh ${prod_user}@${prod_host} 'sudo systemctl restart cameradashboard'
	@echo "cameradashboard restarted"

#-----------------------------------------------------------------------------#
#--- go2rtc (Production)
#-----------------------------------------------------------------------------#

go2rtc_version = 1.9.14

## prod-download-go2rtc: download go2rtc binary for linux/amd64
.PHONY: prod-download-go2rtc
prod-download-go2rtc:
	@echo "Downloading go2rtc ${go2rtc_version} for linux/amd64..."
	curl -L -o ./bin/go2rtc "https://github.com/AlexxIT/go2rtc/releases/download/v${go2rtc_version}/go2rtc_linux_amd64"
	chmod +x ./bin/go2rtc
	@echo "go2rtc downloaded to ./bin/go2rtc"

## prod-deploy-go2rtc: deploy go2rtc binary + config to production
.PHONY: prod-deploy-go2rtc
prod-deploy-go2rtc:
	@test -f ./bin/go2rtc || (echo "Error: ./bin/go2rtc not found. Run 'make prod-download-go2rtc' first." && exit 1)
	@echo "Deploying go2rtc to production..."
	rsync -P ./bin/go2rtc ${prod_user}@${prod_host}:${prod_dir}/
	@echo "go2rtc binary deployed."
	@echo ""
	@echo "NOTE: Deploy go2rtc.yaml with 'make prod-deploy-go2rtc-config'"
	@echo "      go2rtc.yaml MUST use 'listen: 127.0.0.1:1984' for security."
	@echo "      All external access goes through CameraDashboard's auth proxy."

## prod-deploy-go2rtc-config: deploy go2rtc.yaml to production
.PHONY: prod-deploy-go2rtc-config
prod-deploy-go2rtc-config:
	@test -f ../configs/go2rtc.yaml || (echo "Error: ../configs/go2rtc.yaml not found." && exit 1)
	@echo "Deploying go2rtc.yaml to production..."
	rsync -P ../configs/go2rtc.yaml ${prod_user}@${prod_host}:${prod_dir}/config/
	@echo "go2rtc config deployed."

## prod-install-go2rtc: install the go2rtc systemd service (first-time setup)
.PHONY: prod-install-go2rtc
prod-install-go2rtc:
	@echo "Installing go2rtc.service on production..."
	rsync -P ./go2rtc.service ${prod_user}@${prod_host}:/tmp/go2rtc.service
	ssh ${prod_user}@${prod_host} 'sudo mv /tmp/go2rtc.service /etc/systemd/system/go2rtc.service && sudo systemctl daemon-reload && sudo systemctl enable go2rtc'
	@echo "go2rtc service installed and enabled."

## prod-go2rtc-status: check go2rtc service status on production
.PHONY: prod-go2rtc-status
prod-go2rtc-status:
	@ssh ${prod_user}@${prod_host} 'sudo systemctl status go2rtc --no-pager'

## prod-go2rtc-logs: view go2rtc logs on production (follows)
.PHONY: prod-go2rtc-logs
prod-go2rtc-logs:
	ssh -t ${prod_user}@${prod_host} 'sudo journalctl --unit=go2rtc --since="24 hours ago" --follow'

## prod-go2rtc-restart: restart go2rtc on production
.PHONY: prod-go2rtc-restart
prod-go2rtc-restart:
	ssh ${prod_user}@${prod_host} 'sudo systemctl restart go2rtc'
	@echo "go2rtc restarted"

## prod-check-deps: ensure required packages are installed on production
.PHONY: prod-check-deps
prod-check-deps:
	@echo "Checking dependencies on production..."
	@ssh ${prod_user}@${prod_host} 'which ffmpeg > /dev/null 2>&1 || (echo "Installing ffmpeg..." && sudo apt-get update -qq && sudo apt-get install -y -qq ffmpeg)'
	@echo "Dependencies OK."

## prod-check: verify both cameradashboard and go2rtc are running on production
.PHONY: prod-check
prod-check:
	@echo "=== cameradashboard ==="
	@ssh ${prod_user}@${prod_host} 'sudo systemctl is-active cameradashboard && echo "OK" || echo "NOT RUNNING"'
	@echo ""
	@echo "=== go2rtc ==="
	@ssh ${prod_user}@${prod_host} 'sudo systemctl is-active go2rtc && echo "OK" || echo "NOT RUNNING"'
	@echo ""
	@echo "=== ffmpeg ==="
	@ssh ${prod_user}@${prod_host} 'which ffmpeg > /dev/null 2>&1 && ffmpeg -version 2>&1 | head -1 || echo "NOT INSTALLED"'
	@echo ""
	@echo "=== Ports ==="
	@ssh ${prod_user}@${prod_host} 'ss -tlnp 2>/dev/null | grep -E ":(8082|1984) " || echo "No matching ports found"'

#-----------------------------------------------------------------------------#
#--- Git Workflow
#-----------------------------------------------------------------------------#

## git-status: show git status and changes
.PHONY: git-status
git-status:
	@echo "=== Git Status ==="
	@git status --short
	@echo "\n=== Recent Commits ==="
	@git log --oneline -5

## git-ready: prepare code for commit (format, test, audit)
.PHONY: git-ready
git-ready: tidy test audit
	@echo "Code is ready for commit"
	@git status --short

## git-commit: commit changes with message (usage: make git-commit msg="message")
.PHONY: git-commit
git-commit: git-ready
	@if [ -z "$(msg)" ]; then \
		echo "Error: Please provide a commit message using msg='your message'"; \
		echo "Usage: make git-commit msg='your commit message'"; \
		exit 1; \
	fi
	git add -A
	git commit -m "$(msg)"

## git-push: push to origin main (with safety check)
.PHONY: git-push
git-push:
	@echo "Pushing to origin/main..."
	@git pull --rebase origin main
	@git push origin main

## git-sync: ready, commit, and push in one command (usage: make git-sync msg="message")
.PHONY: git-sync
git-sync: git-ready
	@if [ -z "$(msg)" ]; then \
		echo "Error: Please provide a commit message using msg='your message'"; \
		echo "Usage: make git-sync msg='your commit message'"; \
		exit 1; \
	fi
	git add -A
	git commit -m "$(msg)"
	git pull --rebase origin main
	git push origin main
	@echo "Changes synced to remote"

## git-amend: amend the last commit (useful for fixing typos)
.PHONY: git-amend
git-amend: git-ready
	git add -A
	git commit --amend --no-edit

## git-undo: undo last commit but keep changes
.PHONY: git-undo
git-undo:
	git reset --soft HEAD~1
	@echo "Last commit undone, changes kept in working directory"
