#!/usr/bin/env bash
# Test-only fake Docker and /opt boundary. All files stay in TGCLOUD_TEST_DIR.
set -Eeuo pipefail

realpath() { printf '%s\n' /opt/tgcloud-test; }
cd() { builtin cd "$TGCLOUD_TEST_DIR"; }
install() {
  [[ "${1:-}" != -d ]] || return 0
  command install "$@"
}
mktemp() { command mktemp "$TGCLOUD_TEST_DIR/backup-XXXXXXXX"; }
python3() { "$TGCLOUD_TEST_PYTHON" "$@"; }
docker() {
  printf '%s\n' "$*" >> "$TGCLOUD_TEST_DIR/calls"
  case "$*" in
    'compose config --quiet')
      [[ "$TGCLOUD_TEST_MODE" != interrupt ]] || kill -TERM "$$"
      [[ "$TGCLOUD_TEST_MODE" != invalid ]] || return 1
      ;;
    'compose config --format json') printf '%s\n' '{}' ;;
    'compose exec -T tgcloud-'*'-bot python -m app.deployment_check'*)
      command cat >/dev/null
      ;;
    inspect*)
      if [[ "$*" == *'.State.Running'* ]]; then
        printf '%s\n' true
        return 0
      fi
      if [[ "$TGCLOUD_TEST_MODE" == unhealthy ]] && [[ "$(<.env)" == new ]]; then
        printf '%s\n' unhealthy
      else
        printf '%s\n' healthy
      fi
      ;;
  esac
  return 0
}
curl() {
  if [[ "${TGCLOUD_TEST_ACTION:-}" == update ]]; then return 0; fi
  command curl "$@"
}

export INSTALL_DIR=/opt/tgcloud-test
# shellcheck disable=SC1090
source "$TGCLOUD_TEST_SCRIPT" "${TGCLOUD_TEST_ACTION:-apply-config}" "$TGCLOUD_TEST_CANDIDATE"
