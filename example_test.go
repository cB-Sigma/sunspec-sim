// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sunspec_test

import (
	"fmt"
	"log"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// Find the SunSpec map of the simulated inverter over Modbus TCP, read
// who it is and decode its inverter model.
func Example() {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	dev, err := sim.Start(demo.Image, "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()

	r, err := mbtcp.Dial(dev.URL(), 1, 2*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()

	m, err := sunspec.Find(r)
	if err != nil {
		log.Fatal(err)
	}
	for _, inst := range m.Models {
		fmt.Println(inst.ID, inst.Name, inst.L)
	}
	id, _ := sunspec.ReadIdentity(r, m)
	fmt.Println(id.Manufacturer, id.Model, id.Serial)

	inv := m.Model(103)
	regs, err := sunspec.ReadModel(r, *inv)
	if err != nil {
		log.Fatal(err)
	}
	for _, v := range sunspec.Decode(*inv, regs) {
		switch v.Name {
		case "W", "Hz", "PhVphA", "St":
			fmt.Printf("%s.%s = %v %s\n", inv.Name, v.Name, v.V, v.Unit)
		}
	}
	// Output:
	// 1 common 66
	// 103 inverter 50
	// 120 nameplate 26
	// 160 mppt 48
	// 704 derControls 65
	// 715 derControl 7
	// 64900 model64900 3
	// Example Solar ES-10K-3P DEMO000001
	// inverter.PhVphA = 230.1 V
	// inverter.W = 9800 W
	// inverter.Hz = 50.01 Hz
	// inverter.St = MPPT
}

// Plan an output limit of 60 % on the simulated inverter (model 704),
// write it and read it back.
func Example_planControl() {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	dev, err := sim.Start(demo.Image, "")
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()
	r, err := mbtcp.Dial(dev.URL(), 1, 2*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil {
		log.Fatal(err)
	}

	pct := 60.0
	plan, err := sunspec.PlanControl(m, sunspec.Control{Action: sunspec.ActionLimitPower, Percent: &pct},
		func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) })
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range plan.Writes {
		addr, words, err := w.Encode()
		if err != nil {
			log.Fatal(err)
		}
		if err := r.WriteHolding(addr, words); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("model %d: %s = %d at %d\n", w.Model.ID, w.Point, w.Raw, addr)
	}
	regs, err := sunspec.ReadModel(r, *m.Model(plan.Model))
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range plan.Writes {
		got, _ := w.ReadBack(regs)
		fmt.Printf("%s kept: %v\n", w.Point, got == w.Raw)
	}
	// Output:
	// model 704: WMaxLimPct = 600 at 40215
	// model 704: WMaxLimPctEna = 1 at 40214
	// WMaxLimPct kept: true
	// WMaxLimPctEna kept: true
}
