# Conventions
- CLI stdout is only Result JSON, including help and errors; no interactive authentication prompts.
- Preserve exact remote command strings; never tokenize remote code. Global flags precede command.
- Context-aware App operations return Result, with structured Problem and nullable remote exit code.
- Default tests use local process fixtures, never real VPS. Linux shell-specific checks skip macOS.
- User-facing changes update README.md, skills/vpsctl/references/commands.md and CHANGELOG.md together. CONTRIBUTING.md owns contribution rules.
