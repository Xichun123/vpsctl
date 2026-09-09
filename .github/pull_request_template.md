## Summary

Describe the behavior changed and why.

## Validation

- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `go build -o dist/vpsctl .`
- [ ] Skill package check run when `skills/vpsctl/` changed
- [ ] Documentation and changelog updated for user-facing changes
- [ ] No credentials, private keys, host inventories, or production secrets included
