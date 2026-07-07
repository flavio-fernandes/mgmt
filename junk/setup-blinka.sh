#!/bin/bash
# Provision Adafruit Blinka + MCP2221 (hidapi) support inside the guest.
set -e
export DEBIAN_FRONTEND=noninteractive

echo "== apt packages =="
apt-get update -qq
apt-get install -y -qq libhidapi-hidraw0 python3-venv python3-pip

echo "== blacklist kernel hid_mcp2221 so Blinka/hidapi owns the device =="
echo 'blacklist hid_mcp2221' > /etc/modprobe.d/blacklist-mcp2221.conf

echo "== udev rule for non-root access =="
cat > /etc/udev/rules.d/99-mcp2221.rules <<'EOF'
SUBSYSTEM=="usb", ATTRS{idVendor}=="04d8", ATTRS{idProduct}=="00dd", MODE="0666"
KERNEL=="hidraw*", ATTRS{idVendor}=="04d8", ATTRS{idProduct}=="00dd", MODE="0666"
EOF
udevadm control --reload-rules 2>/dev/null || true

echo "== Blinka env vars, system-wide =="
if ! grep -q BLINKA_MCP2221 /etc/environment; then
  printf 'BLINKA_MCP2221=1\nBLINKA_MCP2221_RESET_DELAY=-1\n' >> /etc/environment
fi

echo "== python venv + adafruit-blinka =="
python3 -m venv /opt/blinka
/opt/blinka/bin/pip install --upgrade pip -q
/opt/blinka/bin/pip install -q adafruit-blinka

echo "== verify install (without triggering device detection) =="
/opt/blinka/bin/python -c "import hid; print('hidapi python binding OK')"
/opt/blinka/bin/pip show adafruit-blinka | grep -E '^(Name|Version):'
echo "SETUP_DONE"
