# pty bridge: python3 bridge.py COLS ROWS XPIX YPIX -- cmd args...
import os, sys, pty, fcntl, termios, struct, select
cols, rows, xp, yp = map(int, sys.argv[1:5]); cmd = sys.argv[6:]
pid, fd = pty.fork()
if pid == 0:
    os.execvpe(cmd[0], cmd, os.environ)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, xp, yp))
os.kill(pid, 28)  # SIGWINCH
inp = sys.stdin.fileno(); out = sys.stdout.fileno()
while True:
    r, _, _ = select.select([fd, inp], [], [])
    if fd in r:
        try: d = os.read(fd, 65536)
        except OSError: break
        if not d: break
        os.write(out, d)
    if inp in r:
        d = os.read(inp, 65536)
        if not d: break
        os.write(fd, d)
