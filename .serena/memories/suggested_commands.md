# Suggested commands
- go build -o dist/vpsctl . ; ./dist/vpsctl --version
- go test -race ./... ; go vet ./...
- scripts/check-skill-package.sh requires skills CLI; validates isolated Skill installation, not CLI installation.
- Real SSH integration is opt-in isolated Linux root only: VPSCTL_INTEGRATION=1 go test -run '^TestSSHIntegration$' -v . ; never use production SSH configuration.
