#!/usr/bin/env python3
"""Send bounded terminal queries and compare the replies with GUI metrics."""
import base64
import json
import os
import select
import sys
import termios
import time
import tty

expected = json.loads(sys.argv[1])
fd = sys.stdin.fileno()
previous = termios.tcgetattr(fd)
results = []

def query(sequence, reply):
    os.write(sys.stdout.fileno(), sequence.encode())
    received = b""
    deadline = time.monotonic()+2
    while len(received)<len(reply.encode()) and time.monotonic()<deadline:
        readable,_,_ = select.select([fd],[],[],max(0,deadline-time.monotonic()))
        if readable: received += os.read(fd,4096)
    assert received == reply.encode(), (sequence,received,reply)
    results.append(sequence.encode().hex())

try:
    tty.setraw(fd)
    rows,cols = expected["rows"],expected["columns"]
    cw,ch = expected["cell_width"],expected["cell_height"]
    sw,sh = expected["screen_size_pixels"]
    width,height = expected["frame_size"]
    x,y = expected["window_position_pixels"]
    tx,ty = expected["text_position_pixels"]
    for sequence,reply in (
        ("\x1b[18t",f"\x1b[8;{rows};{cols}t"),
        ("\x1b[14t",f"\x1b[4;{rows*ch};{cols*cw}t"),
        ("\x1b[14;2t",f"\x1b[4;{height};{width}t"),
        ("\x1b[16t",f"\x1b[6;{ch};{cw}t"),
        ("\x1b[15t",f"\x1b[5;{sh};{sw}t"),
        ("\x1b[19t",f"\x1b[9;{sh//ch};{sw//cw}t"),
        ("\x1b[13t",f"\x1b[3;{x&65535};{y&65535}t"),
        ("\x1b[13;2t",f"\x1b[3;{tx&65535};{ty&65535}t"),
        ("\x1b[11t","\x1b[1t"),
        ("\x1b[5n","\x1b[0n"),
        ("\x1b[?996n","\x1b[?997;1n"),
        ("\x1b]0;Water 查询验证\x1b\\\x1b[21t","\x1b]lWater 查询验证\x1b\\"),
        ("\x1b[20t","\x1b]LWater 查询验证\x1b\\"),
        ("\x1bP+q436f;524742\x1b\\","\x1bP1+r436f=323536\x1b\\\x1bP1+r524742=38\x1b\\"),
        ("\x1b[2;4H\x1b[6n","\x1b[2;4R"),
        ("\x1b[?6n","\x1b[?2;4;1R"),
        ("\x1b[1;38;2;1;2;3m\x1bP$qm\x1b\\","\x1bP1$r0;1;38;2;1;2;3m\x1b\\"),
        ("\x1b]52;c;?\x1b\\","\x1b]52;c;\x1b\\"),
    ):
        query(sequence,reply)
    os.write(sys.stdout.fileno(),b"\x1b[0m\x1b]7;file://localhost/tmp/water-query-test\x1b\\")
    data = base64.b64encode(b"WATER_OSC52_COPY_TEST").decode()
    os.write(sys.stdout.fileno(),f"\x1b]52;c;{data}\x1b\\".encode())
finally:
    termios.tcsetattr(fd,termios.TCSANOW,previous)
print(f"\r\nWATER_QUERY_PASS {len(results)} queries grid={cols}x{rows}",flush=True)
