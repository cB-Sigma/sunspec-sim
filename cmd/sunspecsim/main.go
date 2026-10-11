// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command sunspecsim serves a simulated SunSpec inverter over Modbus TCP,
// so a SunSpec client can be tried without a device. The demo inverter
// (sim.DemoInverter) is a made-up 10 kW three-phase unit with the common
// model (1), the inverter model (103), the nameplate (120), two MPPT
// inputs (160), the DER controls 704 and 715 (or 123 with -legacy), a
// vendor model (64900) and the end model. For example:
//
//	sunspecsim -listen 127.0.0.1:5020 -unit 1 -base 40000
//
// With -vary, the inverter's output power changes at that interval.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cB-Sigma/sunspec-sim/sim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sunspecsim:", err)
		os.Exit(1)
	}
}

// run serves until ctx ends. ready, when not nil, receives the device and
// its demo image once it listens.
func run(ctx context.Context, args []string, out io.Writer, ready func(*sim.Device, sim.Demo)) error {
	fs := flag.NewFlagSet("sunspecsim", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", "127.0.0.1:5020", "address to serve Modbus TCP on (port 0: a free port)")
	unit := fs.Int("unit", 0, "the only Modbus unit to answer on, 1 to 247 (0: every unit)")
	base := fs.Int("base", 40000, "address of the SunSpec marker: 40000, 50000 or 0")
	legacy := fs.Bool("legacy", false, "use the controls model 123 instead of 704 and 715")
	vary := fs.Duration("vary", 0, "change the output power at this interval (0: never)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	switch *base {
	case 40000, 50000, 0:
	default:
		return fmt.Errorf("-base %d is not 40000, 50000 or 0", *base)
	}
	if *unit < 0 || *unit > 247 {
		return fmt.Errorf("-unit %d is out of range", *unit)
	}
	demo := sim.DemoInverter(sim.DemoOptions{Base: uint16(*base), Legacy: *legacy})
	var units []uint8
	if *unit != 0 {
		units = append(units, uint8(*unit))
	}
	dev, err := sim.Start(demo.Image, *listen, units...)
	if err != nil {
		return err
	}
	defer dev.Close()
	unitText := "every unit"
	if *unit != 0 {
		unitText = fmt.Sprintf("unit %d", *unit)
	}
	controls := "704, 715"
	if *legacy {
		controls = "123"
	}
	fmt.Fprintf(out, "sunspecsim: SunSpec inverter on %s (%s), marker at %d; models 1, 103, 120, 160, %s, 64900\n", dev.URL(), unitText, *base, controls)
	if ready != nil {
		ready(dev, demo)
	}
	var tick <-chan time.Time
	if *vary > 0 {
		t := time.NewTicker(*vary)
		defer t.Stop()
		tick = t.C
	}
	steps := []uint16{980, 870, 640, 910} // W in tens of watts (W_SF 1)
	for i := 0; ; {
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
			i = (i + 1) % len(steps)
			dev.Set(demo.Inverter+12, steps[i])
		}
	}
}
