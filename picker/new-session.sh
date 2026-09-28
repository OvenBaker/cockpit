# Helpers for the interactive new-session flow. No tmux mutations here.

cockpit_new_launch() {
  local agent="$1" cwd="$2" name="${3:-}" permissions="${4:-auto}"
  case "$permissions" in auto|bypass) ;; *) echo "Unknown launch permission mode" >&2; return 1;; esac
  case "$agent" in
    claude)
      if [[ "$permissions" == bypass ]]; then
        printf 'exec claude --dangerously-skip-permissions%s' "$(cockpit_rc_args "$name" "$cwd")"
      else
        printf 'exec claude --permission-mode auto%s' "$(cockpit_rc_args "$name" "$cwd")"
      fi
      ;;
    codex)
      if [[ "$permissions" == bypass ]]; then
        # Remote TUI permission flags are rejected. This invocation is standalone;
        # never change the shared server's policy for other panes.
        printf 'exec codex --dangerously-bypass-approvals-and-sandbox'
      else
        printf 'exec codex%s' "$(cockpit_codex_launch_args "$cwd")"
      fi
      ;;
    shell) printf 'exec bash -l';;
    *) return 1;;
  esac
}

cockpit_new_directories() {
  local current="$1" repo d label="current repository"
  local -A seen=()
  repo=$(git -C "$current" rev-parse --show-toplevel 2>/dev/null) || { repo="$current"; label="current directory"; }
  if [[ -d "$repo" ]]; then
    printf '%s\t%s\t%s\n' "$repo" "${repo/#$HOME/\~}" "$label"
    seen["$repo"]=1
  fi
  if [[ -d "$current" && -z "${seen[$current]:-}" ]]; then
    printf '%s\t%s\tcurrent directory\n' "$current" "${current/#$HOME/\~}"
    seen["$current"]=1
  fi
  while IFS= read -r d; do
    [[ -n "$d" && -z "${seen[$d]:-}" ]] || continue
    printf '%s\t%s\t\n' "$d" "${d/#$HOME/\~}"
    seen["$d"]=1
  done < <(cockpit_recent_cwds 200)
  printf '+type\tbrowse / type a path…\tTab completion\n'
}

cockpit_read_directory() {
  local start="$1" line cand decoded
  while true; do
    # Readline supplies filesystem completion and editing, without evaluating the
    # entered text as shell code. Spaces remain literal; tilde is the only expansion.
    printf '  path (Tab completes · Ctrl-U clears · empty cancels): ' >/dev/tty
    IFS= read -er -i "${start%/}/" line </dev/tty || return 1
    [[ -n "${line// }" ]] || return 1
    cand="${line/#\~/$HOME}"
    [[ "$cand" == /* ]] || cand="$start/$cand"
    if [[ ! -d "$cand" ]]; then
      # Readline escapes spaces and punctuation when completing a filename. Try
      # that representation only after the literal path; read removes backslash
      # quoting without executing substitutions or evaluating any shell syntax.
      IFS= read decoded <<< "$cand" || true
      [[ -d "$decoded" ]] && cand="$decoded"
    fi
    if [[ -d "$cand" ]]; then
      (cd -- "$cand" && pwd -P)
      return
    fi
    printf '  not a directory: %s\n' "$cand" >/dev/tty
  done
}
