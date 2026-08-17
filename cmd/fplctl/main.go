// Command fplctl is the operational counterpart to fpl-mcp: it captures the
// gameweek snapshots and cached live results the weight optimizer needs, and
// exposes the backtest/evaluate/audit tooling used to validate the
// algorithms against reality. It implements the corresponding operational
// workflows for snapshot, optimization, backtest, evaluation, and audit.
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "snapshot":
		err = runSnapshot(ctx, args)
	case "optimize":
		err = runOptimize(ctx, args)
	case "backtest":
		err = runBacktest(ctx, args)
	case "evaluate":
		err = runEvaluate(ctx, args)
	case "audit":
		err = runAudit(ctx, args)
	case "gengolden":
		err = runGenGolden(ctx, args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "fplctl: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "fplctl %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `fplctl — snapshot, backtest, evaluate, audit, and optimize the FPL algorithms.

Usage:
  fplctl snapshot [--gw N] [--backfill] [--root DIR]
  fplctl optimize [--window N] [--root DIR]
  fplctl backtest [--gw N | --dir DIR --from N --to N | --seasons S1,S2,... --from N --to N [--holdout SEASON] [--elo | --minutes]] [--top N] [--root DIR]
  fplctl evaluate --gw N [--root DIR]
  fplctl audit [--team-id N] [--root DIR]
  fplctl gengolden [--which SET] [--out DIR] | --check

Run 'fplctl <subcommand> -h' for subcommand-specific flags.
`)
}
