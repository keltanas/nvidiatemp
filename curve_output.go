package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"nvidiatemp/internal/config"
	"nvidiatemp/internal/curve"
)

// fmtNum formats a number without trailing zeros (50, 62.5).
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// curveOutput renders the effective configuration: control-loop parameters
// plus the common curve and all per-index overrides, with defaults applied.
func curveOutput(cfg *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "poll interval:    %s\n", cfg.PollInterval)
	fmt.Fprintf(&b, "deadband:         %s%%\n", fmtNum(cfg.DeadbandPct))
	fmt.Fprintf(&b, "write pause:      %s\n", cfg.WritePause)
	fmt.Fprintf(&b, "power threshold:  %s W\n", fmtNum(cfg.PowerThresholdW))
	fmt.Fprintf(&b, "auto delay:       %s\n", cfg.AutoDelay)

	b.WriteString("\ndefault curve:\n")
	writeCurvePoints(&b, cfg.Curve)

	if len(cfg.PerIndex) > 0 {
		idxs := make([]int, 0, len(cfg.PerIndex))
		for idx := range cfg.PerIndex {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			fmt.Fprintf(&b, "\nGPU %d curve:\n", idx)
			writeCurvePoints(&b, cfg.PerIndex[idx])
		}
	}
	return b.String()
}

func writeCurvePoints(b *strings.Builder, c curve.Curve) {
	for _, p := range c {
		fmt.Fprintf(b, "  %s°C → %s%%\n", fmtNum(p.Temp), fmtNum(p.Speed))
	}
}
