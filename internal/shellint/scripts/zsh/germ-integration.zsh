# germ shell integration for zsh, loaded automatically (see .zshenv here).
# To load it by hand elsewhere: source this file from ~/.zshrc. It emits:
#   OSC 133;A/B/C/D  prompt start, input start, output start, command end + status
#   OSC 7            the working directory
[[ -o interactive ]] || return 0
[[ $TERM_PROGRAM == germ || -n $GERM_SHELL_INTEGRATION ]] || return 0
(( ${+__germ_integration} )) && return 0
typeset -g __germ_integration=1
typeset -g __germ_running=

__germ_report_cwd() {
  # Percent-encode what would confuse a URL.
  local p=${PWD//\%/%25}
  p=${p// /%20}; p=${p//\#/%23}; p=${p//\?/%3F}
  printf '\e]7;file://%s%s\a' "${HOST}" "$p"
}

__germ_precmd() {
  local s=$?
  if [[ -n $__germ_running ]]; then
    printf '\e]133;D;%s\a' "$s"
    __germ_running=
  fi
  printf '\e]133;A\a'
  # Input starts after the prompt. The line-init hook below says so too,
  # but frameworks (Oh My Zsh, ...) may replace zle-line-init; prompt
  # themes may rebuild PS1. Between them one survives.
  [[ $PS1 == *'133;B'* ]] || PS1="$PS1"$'%{\e]133;B\a%}'
}

__germ_preexec() {
  __germ_running=1
  printf '\e]133;C\a'
}

# Input starts when the line editor does, which is after any prompt theme
# (powerlevel10k, starship, ...) has drawn its prompt.
__germ_line_init() {
  printf '\e]133;B\a'
}

autoload -Uz add-zsh-hook add-zle-hook-widget
# zsh loads the line editor lazily (at the first prompt), and
# add-zle-hook-widget silently does nothing without it.
zmodload zsh/zle 2>/dev/null
# precmd runs first so it sees the command's exit status.
precmd_functions=(__germ_precmd ${precmd_functions:#__germ_precmd})
add-zsh-hook preexec __germ_preexec
add-zsh-hook chpwd __germ_report_cwd
add-zle-hook-widget line-init __germ_line_init
__germ_report_cwd
