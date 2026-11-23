import os, sys, tty, termios, select
# Usage: keylog.py LOGFILE [setup-sequence]
log = open(sys.argv[1], "w", buffering=1)
fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
if len(sys.argv) > 2:
    os.write(1, sys.argv[2].encode().decode("unicode_escape").encode())
os.write(1, b"ready\r\n")
try:
    while True:
        r, _, _ = select.select([fd], [], [], 0.15)
        if r:
            data = os.read(fd, 1024)
            # group bytes arriving together into one log line
            log.write(repr(data) + "\n")
            seen = (globals().get("seen", b"") + data)[-8:]; globals()["seen"] = seen
            if b"QUIT" in seen:
                break
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, old)
