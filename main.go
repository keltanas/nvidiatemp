// nvidiatemp takes GPU fan cooling under its own control while the card is
// busy (power above a threshold, speed from a temperature curve) and returns
// it to the driver when idle. Subcommands: run (daemon), status, set <pct>,
// curve.
package main

import (
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches subcommands and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest)
	case "status":
		return cmdStatus(rest)
	case "set":
		return cmdSet(rest)
	case "curve":
		return cmdCurve(rest)
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "nvidiatemp: unknown command %q\n\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `nvidiatemp — NVIDIA GPU fan controller

Usage:
  nvidiatemp run    run the control loop as a daemon until SIGINT/SIGTERM
  nvidiatemp status print one-shot per-GPU state (temp, fan, mode, target, power)
  nvidiatemp set PCT set all GPUs to manual mode at PCT percent ([30, 100])
  nvidiatemp set auto return all GPUs to driver (auto) control
  nvidiatemp curve  print the effective curve and control-loop parameters

Flags:
  run    -c PATH  configuration file (default /etc/nvidiatemp.yaml)
         -v       verbose: log every tick (debug level)
  status -c PATH  configuration file (default /etc/nvidiatemp.yaml)
  curve  -c PATH  configuration file (default /etc/nvidiatemp.yaml)
`)
}
