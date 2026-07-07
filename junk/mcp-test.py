#!/opt/blinka/bin/python
"""MCP2221 hello-world via Adafruit Blinka.
Self-contained: sets the Blinka env vars before importing board."""
import os
os.environ.setdefault("BLINKA_MCP2221", "1")
os.environ.setdefault("BLINKA_MCP2221_RESET_DELAY", "-1")

import time
import board
import digitalio

print("Detected board:", board.board_id)

# --- I2C bus scan (non-destructive; empty if nothing wired to SCL/SDA) ---
i2c = board.I2C()
while not i2c.try_lock():
    pass
addrs = [hex(a) for a in i2c.scan()]
i2c.unlock()
print("I2C addresses:", addrs or "(none detected)")

# --- GPIO liveness demo: toggle G0 five times ---
g0 = digitalio.DigitalInOut(board.G0)
g0.direction = digitalio.Direction.OUTPUT
print("Toggling G0 five times (put an LED on G0->GND to see it)...")
for _ in range(5):
    g0.value = True
    time.sleep(0.3)
    g0.value = False
    time.sleep(0.3)

print("OK - MCP2221 is under Blinka control.")
