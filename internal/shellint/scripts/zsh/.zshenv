# germ shell integration bootstrap for zsh. germ points ZDOTDIR here so
# this file runs first; it puts everything back the way it was, sources
# your own .zshenv, and loads the integration. Your .zprofile/.zshrc then
# load from your real ZDOTDIR as usual.
__germ_dir=${${(%):-%x}:A:h}
if [[ $GERM_ORIG_ZDOTDIR == __unset__ ]]; then
  unset ZDOTDIR
else
  ZDOTDIR=$GERM_ORIG_ZDOTDIR
fi
unset GERM_ORIG_ZDOTDIR
# germ's other injections (for fish and bash) aren't for zsh or its children.
if [[ -n ${GERM_ORIG_XDG_DATA_DIRS+x} ]]; then
  if [[ $GERM_ORIG_XDG_DATA_DIRS == __unset__ ]]; then unset XDG_DATA_DIRS; else export XDG_DATA_DIRS=$GERM_ORIG_XDG_DATA_DIRS; fi
  unset GERM_ORIG_XDG_DATA_DIRS
fi
[[ $PROMPT_COMMAND == *germ-integration.bash* ]] && unset PROMPT_COMMAND

if [[ -f ${ZDOTDIR:-$HOME}/.zshenv ]]; then
  source ${ZDOTDIR:-$HOME}/.zshenv
fi
if [[ -o interactive ]]; then
  source $__germ_dir/germ-integration.zsh
fi
unset __germ_dir
