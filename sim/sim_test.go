// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	mb "github.com/simonvetter/modbus"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

func start(t *testing.T, im *sim.Image, units ...uint8) *sim.Device {
	t.Helper()
	d, err := sim.Start(im, "", units...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func dial(t *testing.T, d *sim.Device, unit uint8) *mbtcp.Reader {
	t.Helper()
	r, err := mbtcp.Dial(d.URL(), unit, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestImageLayout(t *testing.T) {
	im := sim.NewImage(50000)
	a := im.Model(1, sim.Str("Maker", 2))
	im.End()
	d := start(t, im)
	r := dial(t, d, 1)
	got, err := r.ReadHolding(50000, 8)
	want := []uint16{0x5375, 0x6E53, 1, 2, 0x4D61, 0x6B65, 0xFFFF, 0}
	if err != nil || !slices.Equal(got, want) || a != 50004 {
		t.Errorf("%#x %v (model at %d)", got, err, a)
	}
	if !slices.Equal(sim.Str("abc", 3), []uint16{0x6162, 0x6300, 0}) || !slices.Equal(sim.Filled(2, 7), []uint16{7, 7}) {
		t.Error("Str/Filled")
	}
}

func TestReadsOutsideTheMap(t *testing.T) {
	im := sim.NewImage(40000)
	im.End()
	r := dial(t, start(t, im), 1)
	for _, c := range [][2]uint16{{39999, 1}, {40004, 1}, {40002, 3}, {0, 2}} {
		_, err := r.ReadHolding(c[0], c[1])
		if !errors.Is(err, mb.ErrIllegalDataAddress) || !r.Refused(err) {
			t.Errorf("read %v: %v, want exception 2", c, err)
		}
	}
}

func TestWritable(t *testing.T) {
	im := sim.NewImage(40000)
	a := im.Model(64000, []uint16{1, 2, 3})
	im.End()
	im.Writable(a, a+1)
	d := start(t, im)
	r := dial(t, d, 1)
	if err := r.WriteHolding(a, []uint16{10, 11}); err != nil {
		t.Fatal(err)
	}
	if d.Get(a) != 10 || d.Get(a+1) != 11 {
		t.Errorf("registers %d %d", d.Get(a), d.Get(a+1))
	}
	// One register that is not writable refuses the whole write.
	for _, w := range []struct {
		addr  uint16
		words []uint16
	}{{a + 1, []uint16{20, 21}}, {a + 2, []uint16{5}}, {40000, []uint16{0}}} {
		if err := r.WriteHolding(w.addr, w.words); !errors.Is(err, mb.ErrIllegalDataAddress) {
			t.Errorf("write %d: %v", w.addr, err)
		}
	}
	if d.Get(a+1) != 11 || d.Get(a+2) != 3 {
		t.Error("a refused write changed registers")
	}
	// The device may keep another value; the log has what was written.
	d.SetKeep(func(addr, v uint16) uint16 {
		if addr == a && v > 100 {
			return 100
		}
		return v
	})
	if err := r.WriteHolding(a, []uint16{500}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.ReadHolding(a, 1)
	if got[0] != 100 {
		t.Errorf("kept %d, want 100", got[0])
	}
	log := d.WriteLog()
	if !slices.Equal(log, []string{fmt.Sprintf("%d=10", a), fmt.Sprintf("%d=11", a+1), fmt.Sprintf("%d=500", a)}) {
		t.Errorf("log %v", log)
	}
	d.SetKeep(nil)
	_ = r.WriteHolding(a, []uint16{500})
	if d.Get(a) != 500 {
		t.Error("hook not cleared")
	}
	// Set changes any register, as the device would.
	d.Set(a+2, 9)
	if got, _ := r.ReadHolding(a+2, 1); got[0] != 9 {
		t.Errorf("set %v", got)
	}
}

func TestUnitsAndTables(t *testing.T) {
	im := sim.NewImage(40000)
	im.End()
	d := start(t, im, 126)
	if _, err := dial(t, d, 126).ReadHolding(40000, 2); err != nil {
		t.Errorf("unit 126: %v", err)
	}
	if _, err := dial(t, d, 1).ReadHolding(40000, 2); !errors.Is(err, mb.ErrGWTargetFailedToRespond) {
		t.Errorf("unit 1: %v", err)
	}
	c, err := mb.NewClient(&mb.ClientConfiguration{URL: d.URL(), Timeout: time.Second})
	if err != nil || c.Open() != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetUnitId(126)
	if _, err := c.ReadRegisters(40000, 1, mb.INPUT_REGISTER); !errors.Is(err, mb.ErrIllegalFunction) {
		t.Errorf("input registers: %v", err)
	}
	if _, err := c.ReadCoils(0, 1); !errors.Is(err, mb.ErrIllegalFunction) {
		t.Errorf("coils: %v", err)
	}
	if _, err := c.ReadDiscreteInputs(0, 1); !errors.Is(err, mb.ErrIllegalFunction) {
		t.Errorf("discrete inputs: %v", err)
	}
}

func TestStartAndClose(t *testing.T) {
	if _, err := sim.Start(sim.NewImage(0), "no-port"); err == nil {
		t.Error("bad address accepted")
	}
	d, err := sim.Start(sim.NewImage(0), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if d.URL() != "tcp://"+d.Addr() {
		t.Errorf("url %s", d.URL())
	}
	if _, err := sim.Start(sim.NewImage(0), d.Addr()); err == nil {
		t.Error("second server on a taken port")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := mbtcp.Dial(d.URL(), 1, 300*time.Millisecond); err == nil {
		t.Error("closed device still accepts connections")
	}
}

// Every point of a NotImplemented model holds its type's not-implemented
// value, so a decoder finds nothing in it.
func TestNotImplemented(t *testing.T) {
	for _, id := range sunspec.Known() {
		def := sunspec.Lookup(id)
		data := sim.NotImplemented(id, 2, 2)
		if def.Len > 0 && len(data) != def.Len {
			t.Errorf("model %d: %d registers, want %d", id, len(data), def.Len)
		}
		if got := sunspec.Decode(sunspec.Instance{ID: id, L: len(data), Def: def}, data); len(got) != 0 {
			var names []string
			for _, v := range got {
				names = append(names, fmt.Sprintf("%s=%v", v.Name, v.V))
			}
			t.Errorf("model %d: decoded %v", id, names)
		}
	}
	if got := sim.NotImplemented(160, 3); len(got) != 8+3*20 {
		t.Errorf("model 160 with three modules: %d registers", len(got))
	}
	if sim.NotImplemented(64900) != nil {
		t.Error("an undescribed model")
	}
}

// The demo leaves out what it does not implement: found by pysunspec2
// (interop), which read 704's WSet as -1 W when the demo filled every
// register with 0xFFFF, a value for the signed types.
func TestDemoNotImplementedPoints(t *testing.T) {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	r := dial(t, start(t, demo.Image), 1)
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, inst := range m.Models {
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range sunspec.Decode(inst, regs) {
			got[inst.Name+"."+v.Name] = v.V
		}
	}
	for _, k := range []string{"derControls.WSet", "derControls.WSetPct", "derControls.VarSet", "nameplate.VArRtgQ1",
		"nameplate.PFRtgQ1", "mppt.1.Tmp", "mppt.2.DCEvt", "inverter.VA", "inverter.DCA", "derControl.DERHb"} {
		if v, ok := got[k]; ok {
			t.Errorf("%s = %v, but the demo does not implement it", k, v)
		}
	}
}

func TestDemoInverter(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		demo := sim.DemoInverter(sim.DemoOptions{Base: 0, Legacy: legacy, Serial: "X1"})
		r := dial(t, start(t, demo.Image), 1)
		m, err := sunspec.Find(r)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int
		for _, inst := range m.Models {
			ids = append(ids, inst.ID)
		}
		want := []int{1, 103, 120, 160, 704, 715, 64900}
		if legacy {
			want = []int{1, 103, 120, 160, 123, 64900}
		}
		if m.Base != 0 || !slices.Equal(ids, want) {
			t.Errorf("legacy %v: %v", legacy, ids)
		}
		if m.Model(103).Addr != demo.Inverter || m.Model(160).Addr != demo.MPPT || m.Model(1).Addr != demo.Common || m.Model(120).Addr != demo.Nameplate {
			t.Error("addresses")
		}
		id, err := sunspec.ReadIdentity(r, m)
		if err != nil || id.Serial != "X1" || id.Manufacturer != "Example Solar" {
			t.Errorf("identity %+v %v", id, err)
		}
		// Every model decodes, with values for the measurements.
		got := map[string]any{}
		for _, inst := range m.Models {
			regs, err := sunspec.ReadModel(r, inst)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range sunspec.Decode(inst, regs) {
				got[inst.Name+"."+v.Name] = v.V
			}
		}
		if got["inverter.W"] != 9800.0 || got["mppt.2.DCV"] != 608.5 || got["nameplate.WRtg"] != 10000.0 {
			t.Errorf("legacy %v: %v", legacy, got)
		}
		if legacy && (got["controls.WMaxLimPct"] != 100.0 || got["controls.Conn"] != "CONNECT") {
			t.Errorf("123: %v %v", got["controls.WMaxLimPct"], got["controls.Conn"])
		}
		if !legacy && (got["derControls.WMaxLimPct"] != 100.0 || got["derControl.OpCtl"] != "START") {
			t.Errorf("704/715: %v %v", got["derControls.WMaxLimPct"], got["derControl.OpCtl"])
		}
	}
}
