// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"math"
	"testing"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

type derBench struct {
	t   *testing.T
	d   *sim.Device
	f   *sim.Follower
	r   *mbtcp.Reader
	m   *sunspec.Map
	der sim.DER
}

func newBench(t *testing.T) *derBench {
	t.Helper()
	der := sim.DERInverter(sim.DEROptions{Base: 40000})
	d := start(t, der.Image)
	f, err := sim.Follow(d, sim.FollowOptions{Grid: sim.Grid{V: 230, Hz: 50, Avail: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	r := dial(t, d, 1)
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	return &derBench{t: t, d: d, f: f, r: r, m: m, der: der}
}

func (b *derBench) read(inst sunspec.Instance) ([]uint16, error) { return sunspec.ReadModel(b.r, inst) }

func (b *derBench) apply(p sunspec.Plan, err error) {
	b.t.Helper()
	if err != nil {
		b.t.Fatal(err)
	}
	for _, w := range p.Writes {
		addr, words, err := w.Encode()
		if err != nil {
			b.t.Fatal(err)
		}
		if err := b.r.WriteHolding(addr, words); err != nil {
			b.t.Fatalf("%s: %v", w.Point, err)
		}
	}
}

func (b *derBench) curve(c sunspec.Curve) { b.t.Helper(); b.apply(sunspec.PlanCurve(b.m, c, b.read)) }

func (b *derBench) set(model int, point string, v any) {
	b.t.Helper()
	inst := b.m.Model(model)
	regs, _ := b.read(*inst)
	w, err := sunspec.PlanWrite(*inst, regs, point, v)
	b.apply(sunspec.Plan{Writes: []sunspec.Write{w}}, err)
}

// values reads a model as a client would.
func (b *derBench) values(model int) map[string]any {
	b.t.Helper()
	inst := b.m.Model(model)
	regs, err := b.read(*inst)
	if err != nil {
		b.t.Fatal(err)
	}
	out := map[string]any{}
	for _, v := range sunspec.Decode(*inst, regs) {
		out[v.Path] = v.V
	}
	return out
}

func (b *derBench) expect(what string, want map[string]float64) {
	b.t.Helper()
	got := b.values(701)
	for k, w := range want {
		f, ok := got[k].(float64)
		if !ok || math.Abs(f-w) > 0.011*math.Max(1, math.Abs(w)) {
			b.t.Errorf("%s: 701.%s = %v, want %v", what, k, got[k], w)
		}
	}
}

var on, off = true, false

// The simulated inverter's output follows the DER settings and the
// adopted curves against the simulated grid: volt-var (705), volt-watt
// (706), frequency droop (711), watt-var (712), the output limit and the
// power factor (704), and stop (715).
func TestFollowCurves(t *testing.T) {
	b := newBench(t)
	b.expect("nothing enabled", map[string]float64{"W": 10000, "Var": 0, "PF": 1, "LNV": 230, "Hz": 50, "VA": 10000})

	// Volt-var: adopted from curve 2, enabled; the first curve now holds it.
	b.curve(sunspec.Curve{Model: 705, Points: [][2]float64{{92, 25}, {98, 0}, {102, 0}, {108, -25}}, Enable: &on})
	vv := b.values(705)
	if vv["AdptCrvRslt"] != "COMPLETED" || vv["Crv.1.Pt.4.V"] != 108.0 || vv["Crv.1.Pt.1.Var"] != 25.0 || vv["Crv.1.ReadOnly"] != "R" {
		t.Errorf("705 after the adopt: %v %v %v %v", vv["AdptCrvRslt"], vv["Crv.1.Pt.4.V"], vv["Crv.1.Pt.1.Var"], vv["Crv.1.ReadOnly"])
	}
	if _, ok := vv["AdptCrvReq"]; ok && vv["AdptCrvReq"] != 0.0 {
		t.Errorf("the adopt request stays at %v", vv["AdptCrvReq"])
	}
	b.expect("volt-var at 100 %", map[string]float64{"Var": 0})
	b.f.SetGrid(sim.Grid{V: 241.5, Hz: 50, Avail: 10000}) // 105 % of 230 V
	// 105 % lies halfway between 102 % (0) and 108 % (-25 % of VarMax 4400 var).
	b.expect("volt-var at 105 %", map[string]float64{"Var": -550, "W": 10000, "LNV": 241.5})
	b.f.SetGrid(sim.Grid{V: 207, Hz: 50, Avail: 10000}) // 90 %: below the first point
	b.expect("volt-var at 90 %", map[string]float64{"Var": 1100})

	// The output limit, then volt-watt below it.
	b.set(704, "WMaxLimPct", 60)
	b.set(704, "WMaxLimPctEna", "ENABLED")
	b.expect("limit 60 %", map[string]float64{"W": 6000, "ThrotPct": 60})
	b.curve(sunspec.Curve{Model: 706, Points: [][2]float64{{100, 100}, {110, 0}}, Enable: &on})
	b.f.SetGrid(sim.Grid{V: 241.5, Hz: 50, Avail: 10000})
	b.expect("volt-watt at 105 %", map[string]float64{"W": 5000, "Var": -550})
	b.set(704, "WMaxLimPctEna", "DISABLED")
	b.curve(sunspec.Curve{Model: 706, Points: [][2]float64{{100, 100}, {110, 0}}, Enable: &off})

	// Frequency droop: 50.5 Hz, dead band 0.036 Hz, droop 5 %: 0.464 Hz
	// over the band takes 0.464 / (50 * 0.05) of 10 kW off.
	b.curve(sunspec.Curve{Model: 711, Set: map[string]any{"DbOf": 0.036, "DbUf": 0.036, "KOf": 0.05, "KUf": 0.05, "PMin": 0}, Enable: &on})
	if v := b.values(711); v["AdptCtlRslt"] != "COMPLETED" || v["Ctl.1.KOf"] != 0.05 {
		t.Errorf("711 after the adopt: %v %v", v["AdptCtlRslt"], v["Ctl.1.KOf"])
	}
	b.f.SetGrid(sim.Grid{V: 230, Hz: 50.5, Avail: 10000})
	b.expect("droop at 50.5 Hz", map[string]float64{"W": 10000 - 1856, "Hz": 50.5})
	b.f.SetGrid(sim.Grid{V: 230, Hz: 50, Avail: 10000})
	b.expect("droop at 50 Hz", map[string]float64{"W": 10000})

	// Watt-var, once volt-var is off: 100 % of WMax gives -30 % of VarMax.
	b.curve(sunspec.Curve{Model: 705, Points: [][2]float64{{92, 25}, {98, 0}, {102, 0}, {108, -25}}, Enable: &off})
	b.curve(sunspec.Curve{Model: 712, Points: [][2]float64{{0, 0}, {50, 0}, {100, -30}}, Enable: &on})
	b.expect("watt-var at full power", map[string]float64{"W": 10000, "Var": -1320})
	b.f.SetGrid(sim.Grid{V: 230, Hz: 50, Avail: 7500})
	b.expect("watt-var at 75 %", map[string]float64{"W": 7500, "Var": -660})

	// A fixed power factor wins over the curves.
	b.set(704, "PFWInj.PF", 0.9)
	b.set(704, "PFWInj.Ext", "UNDER_EXCITED")
	b.set(704, "PFWInjEna", "ENABLED")
	b.expect("PF 0.9 under-excited", map[string]float64{"W": 7500, "Var": -7500 * math.Tan(math.Acos(0.9)), "PF": -0.9})

	// Stop: no output.
	b.set(715, "OpCtl", "STOP")
	b.expect("stopped", map[string]float64{"W": 0, "Var": 0})
	if v := b.values(701); v["InvSt"] != "OFF" {
		t.Errorf("state when stopped: %v", v["InvSt"])
	}
	out := b.f.Output()
	if out.W != 0 {
		t.Errorf("Output %+v", out)
	}
}

// A curve that cannot be adopted (its voltages do not rise) fails, and
// the curve in force stays.
func TestFollowRejectsBadCurve(t *testing.T) {
	b := newBench(t)
	b.curve(sunspec.Curve{Model: 705, Points: [][2]float64{{92, 25}, {98, 0}, {102, 0}, {108, -25}}, Enable: &on})
	b.curve(sunspec.Curve{Model: 705, Index: 3, Points: [][2]float64{{100, 10}, {95, 0}}})
	v := b.values(705)
	if v["AdptCrvRslt"] != "FAILED" || v["Crv.1.Pt.4.V"] != 108.0 {
		t.Errorf("after a bad adopt: %v, first curve point 4 V %v", v["AdptCrvRslt"], v["Crv.1.Pt.4.V"])
	}
	// An index past the stored curves fails as well.
	b.set(705, "AdptCrvReq", 9)
	if v := b.values(705); v["AdptCrvRslt"] != "FAILED" {
		t.Errorf("adopting curve 9: %v", v["AdptCrvRslt"])
	}
	if _, err := sim.Follow(start(t, sim.NewImage(40000)), sim.FollowOptions{}); err == nil {
		t.Error("followed a map without a DER measurement model")
	}
}
