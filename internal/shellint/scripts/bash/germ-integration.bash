# germ shell integration for bash. germ loads it through PROMPT_COMMAND at
# the first prompt; to load it by hand, source it from ~/.bashrc. It emits:
#   OSC 133;A/B/C/D  prompt start, input start, output start, command end + status
#   OSC 7            the working directory
[[ $- == *i* ]] || return 0
[[ $TERM_PROGRAM == germ || -n $GERM_SHELL_INTEGRATION ]] || return 0

# germ's bootstrap PROMPT_COMMAND (and fish's XDG_DATA_DIRS change) are
# done; don't pass them on.
if [[ $PROMPT_COMMAND == *germ-integration.bash* ]]; then
  PROMPT_COMMAND=
  export -n PROMPT_COMMAND
fi
if [[ -n ${GERM_ORIG_XDG_DATA_DIRS+x} ]]; then
  if [[ $GERM_ORIG_XDG_DATA_DIRS == __unset__ ]]; then unset XDG_DATA_DIRS; else export XDG_DATA_DIRS=$GERM_ORIG_XDG_DATA_DIRS; fi
  unset GERM_ORIG_XDG_DATA_DIRS
fi

[[ -n $__germ_integration ]] && return 0
__germ_integration=1
__germ_running=
__germ_at_prompt=
__germ_status=0

__germ_save_status() { __germ_status=$?; }

__germ_precmd() {
  if [[ -n $__germ_running ]]; then
    printf '\e]133;D;%s\a' "$__germ_status"
    __germ_running=
  fi
  local p=${PWD//%/%25}
  p=${p// /%20}; p=${p//#/%23}; p=${p//\?/%3F}
  printf '\e]7;file://%s%s\a' "$HOSTNAME" "$p"
  printf '\e]133;A\a'
  # Prompt tools may rebuild PS1 every time; keep the input-start mark.
  [[ $PS1 == *'133;B'* ]] || PS1="$PS1"'\[\e]133;B\a\]'
  __germ_at_prompt=1
}

__germ_preexec() {
  # The DEBUG trap fires for every simple command; only the first one after
  # a prompt starts the output (and not while completing, or for our hooks).
  [[ -n $COMP_LINE || -z $__germ_at_prompt ]] && return
  case $BASH_COMMAND in __germ_*) return ;; esac
  __germ_at_prompt=
  __germ_running=1
  printf '\e]133;C\a'
}

PROMPT_COMMAND="__germ_save_status${PROMPT_COMMAND:+; $PROMPT_COMMAND}; __germ_precmd"
trap '__germ_preexec' DEBUG
