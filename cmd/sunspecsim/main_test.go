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

// -models all over RTU framing; -models with a list.
func TestRunnerModels(t *testing.T) {
	s, out, stop := runSim(t, "-listen", "127.0.0.1:0", "-models", "all", "-rtu", "-unit", "2")
	r, err := mbtcp.Open(mbtcp.Config{URL: s.dev.URL(), Unit: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	m, err := sunspec.Find(r)
	if err != nil || len(m.Models) != len(sunspec.Known()) || !strings.HasPrefix(s.dev.URL(), "rtuovertcp://") {
		t.Fatalf("all models over RTU: %v %v", err, s.dev.URL())
	}
	_ = r.Close()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "all 112 described models") {
		t.Errorf("output %q", out.String())
	}
	s, _, stop = runSim(t, "-listen", "127.0.0.1:0", "-models", "1,705,707")
	r, err = mbtcp.Dial(s.dev.URL(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m, err = sunspec.Find(r)
	if err != nil || len(m.Models) != 3 || !m.Has(707) {
		t.Errorf("listed models: %+v %v", m, err)
	}
	_ = r.Close()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// -models der over ASCII framing, at a raised grid voltage: the volt-var
// curve, once enabled, shapes the reactive power.
func TestRunnerDER(t *testing.T) {
	s, out, stop := runSim(t, "-listen", "127.0.0.1:0", "-models", "der", "-ascii", "-grid-v", "241.5")
	r, err := mbtcp.Open(mbtcp.Config{URL: s.dev.URL(), Unit: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil || !m.Has(701, 705, 711) || !strings.HasPrefix(s.dev.URL(), "asciiovertcp://") {
		t.Fatalf("%+v %v", m, err)
	}
	read := func(i sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, i) }
	on := true
	plan, err := sunspec.PlanCurve(m, sunspec.Curve{Model: 705, Index: 1, Enable: &on}, read)
	if err == nil {
		t.Errorf("planned a write to the read-only curve: %+v", plan)
	}
	plan, err = sunspec.PlanCurve(m, sunspec.Curve{Model: 705, Points: [][2]float64{{92, 44}, {98, 0}, {102, 0}, {108, -44}}, Enable: &on}, read)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range plan.Writes {
		addr, words, _ := w.Encode()
		if err := r.WriteHolding(addr, words); err != nil {
			t.Fatal(err)
		}
	}
	regs, _ := read(*m.Model(701))
	var q any
	for _, v := range sunspec.Decode(*m.Model(701), regs) {
		if v.Name == "Var" {
			q = v.V
		}
	}
	if q != -968.0 { // halfway from 0 at 102 % to -44 % of 4400 var at 108 %
		t.Errorf("Var at 105 %% of 230 V: %v", q)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "DER inverter") {
		t.Errorf("output %q", out.String())
	}
}

func TestRunnerFlags(t *testing.T) {
	for _, args := range [][]string{
		{"-base", "30000"}, {"-unit", "300"}, {"-unit", "-1"}, {"-listen", "nowhere"}, {"-bogus"}, {"extra"},
		{"-models", "1,64900"}, {"-models", "x"}, {"-tls-cert", "nope.pem", "-tls-key", "nope.pem", "-tls-ca", "nope.pem"},
		{"-rtu", "-tls-cert", "a"}, {"-rtu", "-ascii"}, {"-ascii", "-tls-cert", "a"},
	} {
		if err := run(context.Background(), args, &bytes.Buffer{}, nil); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if err := run(context.Background(), []string{"-h"}, &bytes.Buffer{}, nil); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}
