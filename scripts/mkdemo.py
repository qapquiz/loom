#!/usr/bin/env python3
"""Record a short demo gif of loom driving a live herdr-core terminal.

Frames come from `hc read --lines 0` on loom's own pane (its visible screen,
plain text), cropped to 110x32, rendered with ImageMagick.
"""
import subprocess, time, sys, os

OUT = sys.argv[1] if len(sys.argv) > 1 else "docs/demo.gif"
FRAMES = "/tmp/loomgif-frames"
os.makedirs(FRAMES, exist_ok=True)
for f in os.listdir(FRAMES):
    os.remove(os.path.join(FRAMES, f))

def hc(*args):
    return subprocess.run(["hc", *args], capture_output=True, text=True)

def cap(name, settle=0.8):
    time.sleep(settle)
    r = hc("read", "--lines", "0", "loomgif")
    rows = r.stdout.splitlines()
    cropped = [l[:200] for l in rows[:32]]
    cropped += [""] * (32 - len(cropped))
    open(f"{FRAMES}/{name}.txt", "w").write("\n".join(cropped) + "\n")
    print("frame", name)

def keys(*ks):
    for k in ks:
        hc("keys", "loomgif", k)
        time.sleep(0.08)

def type_word(w):
    ks = []
    for word in w.split():
        ks.extend(list(word))
        ks.append("space")
    ks = ks[:-1]
    keys(*ks)

# fresh terminals
hc("kill", "gifdemo")
hc("kill", "loomgif")
time.sleep(0.5)
hc("rm", "gifdemo")
hc("rm", "loomgif")
hc("run", "--bg", "--name", "gifdemo", "bash")
time.sleep(1)
hc("send", "gifdemo", "echo 'loom: a terminal client for herdr-core'")
time.sleep(1.2)
hc("run", "--bg", "--name", "loomgif",
   "sh", "-c", "exec env LOOM_ATTACH=gifdemo /tmp/loom-bin")
time.sleep(3.5)

cap("01-start", 0.5)

# typing goes through loom into the shell
keys("e", "c", "h", "o", "space")
cap("02-typing", 0.4)
type_word("hello from loom")
cap("03-typed", 0.4)
keys("enter")
cap("04-echo", 1.0)

# flood output, watch the live tail
hc("send", "gifdemo", "seq 1 120")
cap("05-flood", 1.0)

# scrollback: page up, indicator, page down
keys("pgup")
cap("06-scrolled", 0.9)
keys("pgdn")
cap("07-live-again", 0.9)

# detach: loom dies, the program survives, reattach shows the same screen
hc("kill", "loomgif")
time.sleep(1.0)
alive = hc("get", "gifdemo").stdout
cap2 = None
hc("run", "--bg", "--name", "loomgif2",
   "sh", "-c", "exec env LOOM_ATTACH=gifdemo /tmp/loom-bin")
time.sleep(3.0)
r = hc("read", "--lines", "0", "loomgif2")
rows = r.stdout.splitlines()
cropped = [l[:200] for l in rows[:32]]
cropped += [""] * (32 - len(cropped))
open(f"{FRAMES}/08-reattach.txt", "w").write("\n".join(cropped) + "\n")
print("frame 08-reattach, program stayed live:", '"state":"live"' in alive or "live" in alive)

# render: dark bg, warm text, monospace
pngs = []
for f in sorted(os.listdir(FRAMES)):
    if not f.endswith(".txt"):
        continue
    stem = f[:-4]
    png = f"{FRAMES}/{stem}.png"
    subprocess.run(["magick", "-size", "1720x660", "xc:#0d0a12",
                    "-font", "Adwaita-Mono", "-pointsize", "15",
                    "-fill", "#d8d2c8", "-kerning", "0",
                    "-annotate", "+24+30", f"@{FRAMES}/{f}",
                    "-bordercolor", "#0d0a12", "-border", "12",
                    png], check=True)
    pngs.append(png)

subprocess.run(["magick", *sum((["-delay", "55", p] for p in pngs), []),
                "-loop", "0", OUT], check=True)
print("wrote", OUT, os.path.getsize(OUT) // 1024, "KB,", len(pngs), "frames")

hc("kill", "loomgif2")
hc("kill", "gifdemo")
time.sleep(0.4)
hc("rm", "loomgif")
hc("rm", "loomgif2")
hc("rm", "gifdemo")
