#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source_skill="$repo_root/skills/vpsctl"

if ! command -v skills >/dev/null 2>&1; then
  printf 'error: skills CLI is required (npm install -g skills)\n' >&2
  exit 1
fi

skill_count=$(find "$repo_root/skills" -name SKILL.md -type f | wc -l | tr -d ' ')
if [[ "$skill_count" != "1" ]]; then
  printf 'error: expected one source skill, found %s\n' "$skill_count" >&2
  exit 1
fi

list_output=$(skills add "$repo_root" --list)
if ! printf '%s\n' "$list_output" | grep -q 'vpsctl'; then
  printf 'error: skills CLI did not discover the vpsctl skill\n' >&2
  exit 1
fi

temp_home=$(mktemp -d)
trap 'rm -rf "$temp_home"' EXIT

HOME="$temp_home" skills add "$repo_root" \
  --skill vpsctl \
  --global \
  --agent universal \
  --agent pi \
  --yes \
  --copy >/dev/null

for install_root in \
  "$temp_home/.agents/skills/vpsctl" \
  "$temp_home/.pi/agent/skills/vpsctl"
do
  if [[ ! -f "$install_root/SKILL.md" ]]; then
    printf 'error: missing installed SKILL.md at %s\n' "$install_root" >&2
    exit 1
  fi

  while IFS= read -r -d '' source_file; do
    relative_path=${source_file#"$source_skill/"}
    installed_file="$install_root/$relative_path"
    if [[ ! -f "$installed_file" ]]; then
      printf 'error: missing companion file %s\n' "$installed_file" >&2
      exit 1
    fi
    cmp "$source_file" "$installed_file"
  done < <(find "$source_skill" -type f -print0)
done

printf 'vpsctl skill discovery and isolated installation passed\n'
