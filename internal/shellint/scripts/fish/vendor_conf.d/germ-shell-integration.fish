# germ shell integration for fish. Loaded automatically when germ starts
# fish (germ prepends its data dir to XDG_DATA_DIRS); never needs to be
# sourced by hand. It emits:
#   OSC 133;A/B/C/D  prompt start, input start, output start, command end + status
#   OSC 7            the working directory

# Undo germ's XDG_DATA_DIRS change so child processes see the original.
if set -q GERM_ORIG_XDG_DATA_DIRS
    if test "$GERM_ORIG_XDG_DATA_DIRS" = __unset__
        set -e XDG_DATA_DIRS
    else
        set -gx XDG_DATA_DIRS $GERM_ORIG_XDG_DATA_DIRS
    end
    set -e GERM_ORIG_XDG_DATA_DIRS
end

status is-interactive; or exit 0
test "$TERM_PROGRAM" = germ; or exit 0
set -q __germ_integration; and exit 0
set -g __germ_integration 1

function __germ_prompt_start --on-event fish_prompt
    # Wrap the prompt function once (it may be defined after this file
    # loads) so the end of the prompt is marked too.
    if not functions -q __germ_orig_fish_prompt; and functions -q fish_prompt
        functions --copy fish_prompt __germ_orig_fish_prompt
        function fish_prompt
            __germ_orig_fish_prompt
            printf '\e]133;B\a'
        end
    end
    printf '\e]133;A\a'
end

function __germ_preexec --on-event fish_preexec
    printf '\e]133;C\a'
end

function __germ_postexec --on-event fish_postexec
    printf '\e]133;D;%s\a' $status
end

function __germ_report_cwd --on-variable PWD
    printf '\e]7;file://%s%s\a' $hostname (string escape --style=url -- $PWD)
end
__germ_report_cwd
