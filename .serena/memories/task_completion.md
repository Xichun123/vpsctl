# Task completion
- Format changed Go files with gofmt; run go test -race ./..., go vet ./..., go build -o dist/vpsctl .
- Skill changes additionally run scripts/check-skill-package.sh (requires skills CLI).
- Regression checks must cover changed behavior without real VPS; preserve unknown outcomes, quoting, strict host verification and safe file replacement.
- Review diff for credentials, real inventories/config and unrelated changes; do not commit without authorization.
