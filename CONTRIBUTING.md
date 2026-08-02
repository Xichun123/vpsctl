# Contributing

Contributions are welcome through GitHub issues and pull requests.

## Development setup

Requirements:

- Python 3.9 or newer
- OpenSSH client
- Git

Create an isolated environment and install the project in editable mode:

```bash
python -m venv .venv
source .venv/bin/activate
python -m pip install --upgrade pip
python -m pip install -e .
```

## Validation

Run the complete local test suite before opening a pull request:

```bash
PYTHONPATH=src python -m unittest discover -s tests -v
PYTHONPATH=src python -m compileall -q src tests
python -m pip wheel . --no-deps --wheel-dir dist
```

When changing `skills/vpsctl/`, install the `skills` CLI and run:

```bash
scripts/check-skill-package.sh
```

Tests must not depend on access to a real VPS. Mock SSH process and network boundaries unless an explicitly documented manual integration check is required.

## Pull requests

- Keep changes focused and avoid unrelated refactors.
- Add regression coverage for behavior changes and bug fixes.
- Update `README.md`, the Skill reference, and `CHANGELOG.md` when user-facing behavior changes.
- Never commit private keys, passwords, tokens, host inventories, or real production configuration.
- Use clear commit messages that state the behavior changed and why.

By contributing, you agree that your contribution is licensed under the MIT License.
