// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"testing"

	"github.com/cB-Sigma/sunspec-sim/sim"
)

// pysunspec2 drives the IEEE 1547-2018 demo inverter: its adopt request
// and enable on the volt-var model, and the output limit on 704; on its
// next scan the simulated output (701) follows them at the simulated
// grid voltage, and the adopt result reads COMPLETED.
func TestDERFollowsPysunspec2(t *testing.T) {
	der := sim.DERInverter(sim.DEROptions{Base: 40000})
	d := start(t, der.Image)
	f, err := sim.Follow(d, sim.FollowOptions{Grid: sim.Grid{V: 241.5, Hz: 50, Avail: 10000}})
	if err != nil {
		t.Fatal(err)
	}
	r := read(t, d, 1, "705:Crv.2.Pt.4.Var=-40", "705:AdptCrvReq=2", "705:Ena=1", "704:WMaxLimPct=60", "704:WMaxLimPctEna=1")
	for _, w := range []string{"705:Crv.2.Pt.4.Var=-40", "705:Ena=1", "704:WMaxLimPctEna=1"} {
		if !r.Writes[w].OK {
			t.Errorf("pysunspec2 write %s: %+v", w, r.Writes[w])
		}
	}
	expect(t, r, map[string]any{"701.W": 10000, "701.Var": 0, "705.Ena": 0})
	r = read(t, d, 1)
	// 105 % of 230 V, halfway from 0 at 102 % to -40 % of 4400 var at 108 %.
	expect(t, r, map[string]any{"701.W": 6000, "701.Var": -880, "701.LNV": 241.5, "705.Ena": 1, "705.AdptCrvRslt": 1,
		"705.AdptCrvReq": 0, "705.Crv.1.Pt.4.Var": -40, "705.Crv.1.ReadOnly": 1, "704.WMaxLimPct": 60})
	if out := f.Output(); out.W != 6000 {
		t.Errorf("Output %+v", out)
	}
}
