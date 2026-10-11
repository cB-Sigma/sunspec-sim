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
// With -vary, the inverter's output power changes at that interval. With
// -models all the device holds every described SunSpec model (nested
// curve groups included) with made-up values, with -models 1,705 just
// those. With -models der it is an IEEE 1547-2018 inverter (models 701 to
// 715) whose output follows its limits, power factor and adopted curves
// against the grid voltage and frequency given with -grid-v and -grid-hz.
// -rtu speaks Modbus RTU framing over TCP (as a serial gateway), -ascii
// Modbus ASCII framing; -tls-cert, -tls-key and -tls-ca serve Modbus TCP
// over TLS with client certificates.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cB-Sigma/sunspec"
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
	models := fs.String("models", "demo", "demo (the demo inverter), der (an IEEE 1547-2018 inverter whose output follows its settings and curves), all (every described model), or model IDs (1,705,707) with made-up values")
	gridV := fs.Float64("grid-v", 0, "with -models der: the simulated grid voltage in V (0: nominal, 230 V)")
	gridHz := fs.Float64("grid-hz", 0, "with -models der: the simulated grid frequency (0: nominal, 50 Hz)")
	rtu := fs.Bool("rtu", false, "speak Modbus RTU framing over TCP (as a serial gateway) instead of Modbus TCP")
	asc := fs.Bool("ascii", false, "speak Modbus ASCII framing over TCP instead of Modbus TCP")
	tlsCert := fs.String("tls-cert", "", "serve Modbus TCP over TLS with this certificate PEM file (with -tls-key and -tls-ca)")
	tlsKey := fs.String("tls-key", "", "the TLS certificate's key PEM file")
	tlsCA := fs.String("tls-ca", "", "CA PEM file client certificates must chain to")
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
	what := "SunSpec inverter"
	controls := "704, 715"
	if *legacy {
		controls = "123"
	}
	list := fmt.Sprintf("models 1, 103, 120, 160, %s, 64900", controls)
	der := false
	switch *models {
	case "demo":
	case "der":
		d := sim.DERInverter(sim.DEROptions{Base: uint16(*base)})
		demo = sim.Demo{Image: d.Image}
		der = true
		what, list = "SunSpec DER inverter", "models 1, 701, 702, 704, 705, 706, 711, 712, 715; output follows its settings"
	case "all":
		im, _, err := sim.AllModels(uint16(*base))
		if err != nil {
			return err
		}
		demo = sim.Demo{Image: im}
		what, list = "SunSpec device", fmt.Sprintf("all %d described models", len(sunspec.Known()))
	default:
		im := sim.NewImage(uint16(*base))
		var ids []string
		for _, s := range strings.Split(*models, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || sunspec.Lookup(id) == nil {
				return fmt.Errorf("-models: %q is not demo, all or a described model", s)
			}
			if _, err := im.Add(id, sim.Example(id)); err != nil {
				return err
			}
			ids = append(ids, strconv.Itoa(id))
		}
		im.End()
		demo = sim.Demo{Image: im}
		what, list = "SunSpec device", "models "+strings.Join(ids, ", ")
	}
	var units []uint8
	if *unit != 0 {
		units = append(units, uint8(*unit))
	}
	opts := sim.Options{Listen: *listen, Units: units, RTU: *rtu, ASCII: *asc}
	if *rtu && *asc {
		return errors.New("-rtu and -ascii do not go together")
	}
	if *tlsCert != "" || *tlsKey != "" || *tlsCA != "" {
		if *rtu || *asc {
			return errors.New("-rtu or -ascii and TLS do not go together")
		}
		pair, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			return err
		}
		pem, err := os.ReadFile(*tlsCA)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("%s holds no PEM certificate", *tlsCA)
		}
		opts.TLSCert, opts.TLSClientCAs = &pair, pool
	}
	dev, err := sim.StartOptions(demo.Image, opts)
	if err != nil {
		return err
	}
	defer dev.Close()
	if der {
		if _, err := sim.Follow(dev, sim.FollowOptions{Grid: sim.Grid{V: *gridV, Hz: *gridHz}}); err != nil {
			return err
		}
	}
	unitText := "every unit"
	if *unit != 0 {
		unitText = fmt.Sprintf("unit %d", *unit)
	}
	fmt.Fprintf(out, "sunspecsim: %s on %s (%s), marker at %d; %s\n", what, dev.URL(), unitText, *base, list)
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
			if demo.Inverter != 0 {
				dev.Set(demo.Inverter+12, steps[i])
			}
		}
	}
}
