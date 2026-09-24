# nvidiatemp

A fan speed controller for NVIDIA GPUs: it takes cooling under its own control
when the card is busy (power draw above a threshold) and returns it to the
driver when idle.

Русская версия: [README_RU.md](README_RU.md)

## How it works

- Every `poll_interval` (default 2s) it reads temperature, power draw and the
  current fan speed of all visible GPUs in a single `nvidia-smi` call.
- Power above the threshold (default 50W) → the GPU is switched to manual mode
  and the fan speed follows a «temperature → speed» curve (piecewise-linear
  interpolation, clamped to [30, 100]).
- Return to auto mode happens only when power is below the threshold **and**
  temperature is below the lower bound of the curve. After that the fan is held
  at its minimum (30%) for `auto_delay` (default 5 minutes) and the switch to
  auto happens if no load appears in the meantime: any violation of the
  conditions (power above the threshold again, or temperature above the lower
  bound) cancels the cooldown and control follows the curve again.
- Anti-flapping: a speed is written only when it differs from the current one
  by at least the deadband (default 5%) and no more often than once per
  `write_pause` (default 5s); mode switches are subject to the same pause.
- Safety: on any daemon stop (SIGINT/SIGTERM, panic, 5 consecutive failed
  polls) all managed GPUs are returned to auto mode.

## Requirements

- Linux with the NVIDIA driver: `nvidia-smi` (reads — no root needed) and
  `nvidia-settings` (speed writes).
- An X display on `DISPLAY=:0` for writing via `nvidia-settings`.
- Root for the systemd service, or passwordless sudo for interactive use
  (sudo is prepended automatically when euid ≠ 0).

## Building

```sh
go build -o nvidiatemp .
```

A single cross-compilable binary; the only external dependency is
`gopkg.in/yaml.v3`.

## Installation (systemd)

```sh
sudo install -m 0755 nvidiatemp /usr/local/bin/nvidiatemp
sudo cp deploy/nvidiatemp.example.yaml /etc/nvidiatemp.yaml   # edit if you like
sudo cp deploy/nvidiatemp.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now nvidiatemp
systemctl status nvidiatemp
```

If X access errors appear in the journal (`journalctl -u nvidiatemp`),
uncomment `Environment=XAUTHORITY=/home/<user>/.Xauthority` in the unit
(or use `xhost +local:`).

## Subcommands

```
nvidiatemp run      daemon: control loop until SIGINT/SIGTERM
nvidiatemp status   one-shot per-GPU report: temperature, speed, mode, target speed from the curve, power
nvidiatemp set PCT  all GPUs to manual mode at PCT percent ([30, 100]); no loop is started
nvidiatemp set auto all GPUs back to auto (driver) control
nvidiatemp curve    effective curve (common + per-index) and loop parameters
```

The `-c <path>` flag sets the configuration file (default `/etc/nvidiatemp.yaml`)
for `run`, `status` and `curve`. `run` has `-v` — log every tick (Debug level).

## Configuration

Format and defaults are documented in the comments of
[`deploy/nvidiatemp.example.yaml`](deploy/nvidiatemp.example.yaml). Without a
file, built-in defaults apply: curve `50→30, 60→40, 65→45, 70→55, 75→60,
80→70`, interval 2s, deadband 5%, pause 5s, power threshold 50W, auto-cooldown
5 minutes. The config is validated at startup (curve temperatures strictly
increasing, speeds within [30, 100], power threshold > 0) — on error the
process exits with code 1 before the loop starts.

## Manual return to auto mode

If the daemon was stopped uncleanly (`kill -9`, power loss), a card may be left
in manual mode. Return it to the driver manually:

```sh
nvidiatemp set auto
# or without nvidiatemp, for each GPU (index as reported by nvidia-smi):
sudo DISPLAY=:0 nvidia-settings -a "[gpu:0]/GPUFanControlState=0"
```

Check the current mode:

```sh
DISPLAY=:0 nvidia-settings -q GPUFanControlState
```

## Rollback

```sh
sudo systemctl disable --now nvidiatemp   # on stop the daemon returns auto mode itself
sudo rm /etc/systemd/system/nvidiatemp.service /usr/local/bin/nvidiatemp
sudo systemctl daemon-reload
```
