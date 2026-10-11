// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// The tests of the sunspec module that drive the simulator over Modbus TCP.
package sunspec_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// serve starts the simulator for a test.
func serve(t testing.TB, im *sim.Image, units ...uint8) *sim.Device {
	t.Helper()
	d, err := sim.Start(im, "", units...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// dialSim connects a Modbus TCP reader to the simulator.
func dialSim(t testing.TB, d *sim.Device, unit uint8) *mbtcp.Reader {
	t.Helper()
	r, err := mbtcp.Dial(d.URL(), unit, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// newInverter lays out a three-phase inverter register by register from
// the published model definitions (offsets counted from the first
// register after ID and L): common (1), inverter (103), nameplate (120),
// MPPT with two inputs (160), the DER AC controls (704), a vendor model
// (64900) and the end model. Not-implemented values (0xFFFF, 0x8000) are
// where the device has nothing.
func newInverter(base uint16) *sim.Image {
	im := sim.NewImage(base)
	common := append(append(append(append(append(sim.Str("Fronius", 16), sim.Str("Symo 10.0-3-M", 16)...),
		sim.Str("", 8)...), sim.Str("1.28.7-1", 8)...), sim.Str("34119102", 16)...), 1, 0x8000)
	im.Model(1, common)

	inv := sim.Filled(50, 0xFFFF)
	inv[0], inv[1], inv[2], inv[3], inv[4] = 125, 42, 41, 42, 0xFFFF // A 12.5, A_SF -1
	inv[8], inv[9], inv[11] = 2301, 2299, 0xFFFF                     // PhVphA/B, V_SF -1; PhVphC not implemented
	inv[12], inv[13] = 800, 1                                        // W 800 × 10^1
	inv[14], inv[15] = 5002, 0xFFFE                                  // Hz 50.02 (Hz_SF -2)
	inv[16], inv[17] = 0x8000, 0                                     // VA not implemented
	inv[22], inv[23], inv[24] = 0x0001, 0x86A0, 0                    // WH 100000 Wh
	inv[29], inv[30] = 8200, 0                                       // DCW
	inv[31], inv[32], inv[35] = 452, 0x8000, 0xFFFF                  // TmpCab 45.2, TmpSnk not implemented
	inv[36] = 4                                                      // St MPPT
	inv[38], inv[39] = 0, 0b10100                                    // Evt1 AC_DISCONNECT, GRID_DISCONNECT
	inv[40], inv[41] = 0, 0
	im.Model(103, inv)

	plate := sim.Filled(26, 0xFFFF)
	plate[0], plate[1], plate[2] = 4, 10000, 0 // PV, WRtg 10000 W
	im.Model(120, plate)

	mppt := sim.Filled(8+2*20, 0xFFFF)
	mppt[0], mppt[1], mppt[2], mppt[3] = 0xFFFE, 0xFFFF, 0, 0 // DCA_SF -2, DCV_SF -1
	mppt[4], mppt[5], mppt[6] = 0, 0, 2                       // Evt, N = 2
	for k, m := range [][3]uint16{{512, 6105, 3120}, {498, 5987, 2980}} {
		o := 8 + 20*k
		mppt[o] = uint16(k + 1)
		copy(mppt[o+1:], sim.Str(fmt.Sprintf("PV%d", k+1), 8))
		mppt[o+9], mppt[o+10], mppt[o+11] = m[0], m[1], m[2]
		mppt[o+12], mppt[o+13] = 0, 0 // DCWH acc32 not accumulated
	}
	im.Model(160, mppt)

	der := sim.Filled(65, 0xFFFF)
	der[0], der[12], der[13] = 0, 0, 1000 // PFWInjEna off, WMaxLimPctEna off, WMaxLimPct 100.0
	der[51], der[52] = 0xFFFD, 0xFFFF     // PF_SF -3, WMaxLimPct_SF -1
	der[57], der[58] = 1000, 0            // PFWInj.PF 1.000 over-excited
	a := im.Model(704, der)
	im.Writable(a+0, a+12, a+13, a+57, a+58)
	im.Model(64900, []uint16{1, 2, 3})
	im.End()
	return im
}

// The library's map test, over Modbus TCP against the simulator: the
// marker at each base address, the chain to the end model, scale
// factors, not-implemented points left out, enums and bitfields named, an
// unknown model kept; a Modbus device without a map is told apart.
//
// Covers: SIG-IMP-053
func TestSunSpecMapDecodeOverModbusTCP(t *testing.T) {
	for _, base := range []uint16{40000, 50000, 0} {
		r := dialSim(t, serve(t, newInverter(base)), 1)
		m, err := sunspec.Find(r)
		if err != nil {
			t.Fatalf("base %d: %v", base, err)
		}
		var ids []int
		for _, inst := range m.Models {
			ids = append(ids, inst.ID)
		}
		if m.Base != base || !slices.Equal(ids, []int{1, 103, 120, 160, 704, 64900}) {
			t.Fatalf("base %d: map %d %v", base, m.Base, ids)
		}
		if unk := m.Model(64900); unk.Def != nil || unk.L != 3 || unk.Name != "model64900" {
			t.Errorf("vendor model %+v", unk)
		}
		id, err := sunspec.ReadIdentity(r, m)
		if err != nil || id.Manufacturer != "Fronius" || id.Model != "Symo 10.0-3-M" || id.Serial != "34119102" || id.Version != "1.28.7-1" || id.DeviceAddress != 1 || id.Options != "" {
			t.Errorf("identity %+v %v", id, err)
		}
	}
	r := dialSim(t, serve(t, newInverter(40000)), 1)
	m, _ := sunspec.Find(r)
	got := map[string]any{}
	for _, inst := range []int{103, 160} {
		in := m.Model(inst)
		regs, err := sunspec.ReadModel(r, *in)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range sunspec.Decode(*in, regs) {
			got[in.Name+"."+v.Name] = v.V
		}
	}
	want := map[string]any{
		"inverter.A": 12.5, "inverter.AphB": 4.1, "inverter.PhVphA": 230.1, "inverter.W": 8000.0, "inverter.Hz": 50.02,
		"inverter.WH": 100000.0, "inverter.DCW": 8200.0, "inverter.TmpCab": 45.2, "inverter.St": "MPPT",
		"mppt.1.DCA": 5.12, "mppt.1.DCV": 610.5, "mppt.2.DCW": 2980.0, "mppt.2.IDStr": "PV2", "mppt.2.ID": 2.0,
	}
	for k, w := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(w) {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	if fmt.Sprint(got["inverter.Evt1"]) != "[AC_DISCONNECT GRID_DISCONNECT]" {
		t.Errorf("Evt1 = %v", got["inverter.Evt1"])
	}
	for _, k := range []string{"inverter.PhVphC", "inverter.VA", "inverter.TmpSnk", "inverter.A_SF", "inverter.PPVphAB", "mppt.1.DCWH"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is not implemented but was decoded: %v", k, got[k])
		}
	}
	plain := sim.NewImage(1000) // marker elsewhere
	plain.End()
	if _, err := sunspec.Find(dialSim(t, serve(t, plain), 1)); err != sunspec.ErrNotSunSpec {
		t.Errorf("no map: %v", err)
	}
}

func f(v float64) *float64 { return &v }

// The whole control path against the simulator: plan, encode, write,
// read back; an inverter that clamps the value shows it.
func TestControlAgainstSim(t *testing.T) {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	dev := serve(t, demo.Image)
	r := dialSim(t, dev, 1)
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	regs := func(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(r, inst) }
	apply := func(c sunspec.Control) map[string]int64 {
		t.Helper()
		p, err := sunspec.PlanControl(m, c, regs)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range p.Writes {
			addr, words, err := w.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.WriteHolding(addr, words); err != nil {
				t.Fatal(err)
			}
		}
		out := map[string]int64{}
		for _, w := range p.Writes {
			b, err := sunspec.ReadModel(r, w.Model)
			if err != nil {
				t.Fatal(err)
			}
			out[w.Point], _ = w.ReadBack(b)
		}
		return out
	}
	if got := apply(sunspec.Control{Action: "limitPower", Percent: f(60)}); got["WMaxLimPct"] != 600 || got["WMaxLimPctEna"] != 1 {
		t.Errorf("limit %v", got)
	}
	if got := apply(sunspec.Control{Action: "disconnect"}); got["OpCtl"] != 0 || dev.Get(demo.DERCtl+6) != 0 {
		t.Errorf("disconnect %v", got)
	}
	dev.SetKeep(func(a, v uint16) uint16 {
		if a == demo.Controls+13 && v < 200 {
			return 200
		}
		return v
	})
	if got := apply(sunspec.Control{Action: "limitPower", Percent: f(5)}); got["WMaxLimPct"] != 200 {
		t.Errorf("clamped %v", got)
	}
	log := dev.WriteLog()
	if !slices.Contains(log, fmt.Sprintf("%d=50", demo.Controls+13)) {
		t.Errorf("write log %v", log)
	}
}
