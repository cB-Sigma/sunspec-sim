// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

type started struct {
	dev  *sim.Device
	demo sim.Demo
}

func runSim(t *testing.T, args ...string) (started, *bytes.Buffer, func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan started, 1)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, args, &out, func(d *sim.Device, demo sim.Demo) { got <- started{d, demo} })
	}()
	select {
	case s := <-got:
		return s, &out, func() error { cancel(); return <-done }
	case err := <-done:
		cancel()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("not ready")
	}
	return started{}, nil, nil
}

func TestRunnerDemo(t *testing.T) {
	s, out, stop := runSim(t, "-listen", "127.0.0.1:0", "-unit", "126", "-base", "50000", "-vary", "20ms")
	r, err := mbtcp.Dial(s.dev.URL(), 126, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, inst := range m.Models {
		ids = append(ids, inst.ID)
	}
	if m.Base != 50000 || !slices.Equal(ids, []int{1, 103, 120, 160, 704, 715, 64900}) {
		t.Errorf("map %d %v", m.Base, ids)
	}
	// Other units are not answered.
	other, err := mbtcp.Dial(s.dev.URL(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sunspec.Find(other); err != sunspec.ErrNotSunSpec {
		t.Errorf("unit 1: %v", err)
	}
	_ = other.Close()
	// -vary changes the output power.
	deadline := time.Now().Add(3 * time.Second)
	for s.dev.Get(s.demo.Inverter+12) == 980 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.dev.Get(s.demo.Inverter+12) == 980 {
		t.Error("output power did not change")
	}
	_ = r.Close()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unit 126") || !strings.Contains(out.String(), "marker at 50000") || !strings.Contains(out.String(), "704, 715") {
		t.Errorf("output %q", out.String())
	}
}

func TestRunnerLegacy(t *testing.T) {
	s, out, stop := runSim(t, "-listen", "127.0.0.1:0", "-legacy")
	r, err := mbtcp.Dial(s.dev.URL(), 3, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil || m.Base != 40000 || !m.Has(123) || m.Has(704) {
		t.Fatalf("%+v %v", m, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "every unit") || !strings.Contains(out.String(), ", 123,") {
		t.Errorf("output %q", out.String())
	}
}

func TestRunnerFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-base", "30000"}, {"-unit", "300"}, {"-unit", "-1"}, {"-listen", "nowhere"}, {"-bogus"}, {"extra"},
	} {
		if err := run(context.Background(), args, &bytes.Buffer{}, nil); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if err := run(context.Background(), []string{"-h"}, &bytes.Buffer{}, nil); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}
