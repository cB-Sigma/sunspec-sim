// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// flatten turns pysunspec2's nested values into paths ("Crv.2.Pt.1.V").
func flatten(prefix string, v any, out map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			flatten(prefix+k+".", e, out)
		}
	case []any:
		for i, e := range x {
			flatten(fmt.Sprintf("%s%d.", prefix, i+1), e, out)
		}
	default:
		out[strings.TrimSuffix(prefix, ".")] = v
	}
}

// pysunspec2 scans the simulator holding every described model (two
// occurrences of every repeating group, nested curve groups included)
// over Modbus TCP: it finds the whole chain at the simulator's addresses
// without a model error and decodes every point as the sunspec module
// does.
func TestAllModels(t *testing.T) {
	im, at, err := sim.AllModels(40000)
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, im)
	rep := read(t, d, 1)
	if len(rep.Models) != len(sunspec.Known()) {
		t.Fatalf("pysunspec2 found %d models, want %d", len(rep.Models), len(sunspec.Known()))
	}
	r, err := mbtcp.Dial(d.URL(), 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	checked, nested := 0, 0
	for i, pm := range rep.Models {
		inst := m.Models[i]
		if pm.ID != inst.ID || pm.Addr != int(at[pm.ID])-2 || pm.Len != inst.L || !pm.Known {
			t.Errorf("pysunspec2 model %d at %d len %d known %v; simulator %d at %d len %d", pm.ID, pm.Addr, pm.Len, pm.Known, inst.ID, at[inst.ID]-2, inst.L)
			continue
		}
		theirs := map[string]any{}
		flatten("", pm.Values, theirs)
		delete(theirs, "ID") // the header, which the sunspec module keeps in Instance
		delete(theirs, "L")
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		ours := map[string]sunspec.Value{}
		for _, v := range sunspec.Decode(inst, regs) {
			ours[v.Path] = v
		}
		placed := map[string]*sunspec.Point{}
		for _, l := range sunspec.Layout(inst, regs) {
			placed[l.Path] = l.Point
		}
		paths := make([]string, 0, len(theirs))
		for p := range theirs {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, path := range paths {
			pv := theirs[path]
			p, ok := placed[path]
			if !ok {
				t.Errorf("model %d: pysunspec2 has %s, we do not", inst.ID, path)
				continue
			}
			if p.Type == "sunssf" || p.Type == "pad" {
				continue
			}
			v, decoded := ours[path]
			switch {
			case pv == nil && decoded:
				t.Errorf("model %d: %s = %v, pysunspec2 not implemented", inst.ID, path, v.V)
			case pv != nil && !decoded:
				t.Errorf("model %d: %s not decoded, pysunspec2 %v", inst.ID, path, pv)
			case pv != nil && !agrees(p, v, pv):
				t.Errorf("model %d: %s = %v (raw %d), pysunspec2 %v", inst.ID, path, v.V, v.Raw, pv)
			case pv != nil:
				checked++
				if strings.Count(path, ".") >= 3 {
					nested++
				}
			}
		}
		for path := range placed {
			if _, ok := theirs[path]; !ok {
				t.Errorf("model %d: we place %s, pysunspec2 does not", inst.ID, path)
			}
		}
	}
	t.Logf("pysunspec2 and the sunspec module agreed on %d values in %d models (%d in nested groups)", checked, len(rep.Models), nested)
}

func agrees(p *sunspec.Point, v sunspec.Value, py any) bool {
	switch {
	case strings.HasPrefix(p.Type, "enum"), strings.HasPrefix(p.Type, "bitfield"):
		f, ok := py.(float64)
		return ok && int64(f) == v.Raw
	case p.Type == "ipaddr", p.Type == "ipv6addr", p.Type == "eui48":
		return true // pysunspec2 gives these as numbers or bytes; the sunspec module's own tests check the text
	}
	if f, ok := py.(float64); ok {
		g, ok := v.V.(float64)
		return ok && math.Abs(g-f) <= 1e-6*math.Max(1, math.Abs(f))
	}
	return fmt.Sprint(v.V) == fmt.Sprint(py)
}

// pysunspec2 writes curve points of the simulator's volt-var (705) and
// trip (707) models, nested groups by path, and the simulator keeps them;
// a write to a curve's read-only flag is refused.
func TestCurveWrites(t *testing.T) {
	im := sim.NewImage(40000)
	if _, err := im.Add(1, map[string]any{"Mn": "Example Solar", "Md": "ES-10K-3P", "SN": "CRV1"}); err != nil {
		t.Fatal(err)
	}
	vv, err := im.Add(705, map[string]any{"Ena": 0, "V_SF": -2, "DeptRef_SF": -1, "RspTms_SF": 0, "Crv": []map[string]any{
		{"ActPt": 2, "ReadOnly": 1, "Pt": []map[string]any{{"V": 9500, "Var": 300}, {"V": 10500, "Var": -300}}},
		{"ActPt": 2, "ReadOnly": 0, "Pt": []map[string]any{{"V": 9600, "Var": 200}, {"V": 10400, "Var": -200}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.Add(707, map[string]any{"V_SF": -1, "Tms_SF": -2, "Crv": []map[string]any{
		{"ReadOnly": 1, "MustTrip": map[string]any{"ActPt": 1, "Pt": []map[string]any{{"V": 500, "Tms": 16}, {}}}},
		{"ReadOnly": 0, "MustTrip": map[string]any{"ActPt": 1, "Pt": []map[string]any{{"V": 450, "Tms": 20}, {}}}},
	}}); err != nil {
		t.Fatal(err)
	}
	im.End()
	d := start(t, im)
	// The simulator takes writes where the definitions allow them; the
	// curves' ReadOnly flag is not one of those points.
	rep := read(t, d, 1, "705:Crv.2.Pt.1.V=97.5", "705:Crv.2.Pt.2.Var=-25", "705:AdptCrvReq=2", "705:Ena=1",
		"707:Crv.2.MustTrip.Pt.2.V=60", "707:Crv.2.MustTrip.Pt.2.Tms=2", "705:Crv.1.ReadOnly=0")
	expect(t, rep, map[string]any{"705.Crv.1.Pt.1.V": 95.0, "705.Crv.2.Pt.2.Var": -20.0, "707.Crv.1.MustTrip.Pt.1.Tms": 0.16, "705.NPt": 2, "707.NCrvSet": 2},
		"707.Crv.1.MustTrip.Pt.2.V")
	expectWrite(t, rep, "705:Crv.2.Pt.1.V=97.5", 97.5)
	expectWrite(t, rep, "705:Crv.2.Pt.2.Var=-25", -25)
	expectWrite(t, rep, "705:AdptCrvReq=2", 2)
	expectWrite(t, rep, "707:Crv.2.MustTrip.Pt.2.V=60", 60)
	expectWrite(t, rep, "707:Crv.2.MustTrip.Pt.2.Tms=2", 2)
	if w := rep.Writes["705:Crv.1.ReadOnly=0"]; w.OK {
		t.Errorf("pysunspec2 wrote the read-only ReadOnly point: %+v", w)
	}
	if got := d.Get(vv + 13 + 14 + 10); got != 9750 {
		t.Errorf("curve 2 point 1 V register = %d, want 9750", got)
	}
	t.Logf("writes the simulator took: %v", d.WriteLog())
}
