// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"

	"github.com/cB-Sigma/sunspec"
)

// DEROptions shapes the IEEE 1547-2018 demo inverter.
type DEROptions struct {
	// Base is the address of the marker (40000, 50000 or 0).
	Base uint16
	// Manufacturer, Model and Serial go into the common model; empty
	// fields get made-up demo values.
	Manufacturer, Model, Serial string
	// WMax is the active power rating in W (0: 10000); VarMax the
	// reactive power rating in var (0: 44 % of WMax); VNom the nominal
	// line-to-neutral voltage in V (0: 230).
	WMax, VarMax, VNom float64
}

// DER is the IEEE 1547-2018 demo inverter's image and the address of
// each model's first data register.
type DER struct {
	// Image is the register map; serve it with Start and make it act
	// with Follow.
	Image *Image
	// At maps a model ID to the address of its first data register.
	At map[int]uint16
}

// sym is the raw value of an enum's symbol, by point name, anywhere in a
// model's definition.
func sym(id int, point, name string) int64 {
	def := sunspec.Lookup(id)
	var find func(pts []sunspec.Point, gs []sunspec.Group) (int64, bool)
	find = func(pts []sunspec.Point, gs []sunspec.Group) (int64, bool) {
		for _, p := range pts {
			if p.Name == point {
				v, ok := p.Symbols[name]
				return v, ok
			}
		}
		for _, g := range gs {
			if v, ok := find(g.Points, g.Groups); ok {
				return v, true
			}
		}
		return 0, false
	}
	v, ok := find(def.Points, def.Groups)
	if !ok {
		panic(fmt.Sprintf("model %d has no symbol %s.%s", id, point, name))
	}
	return v
}

// DERInverter builds an inverter described by the IEEE 1547-2018 models:
// common (1), DER AC measurement (701), capacity (702), AC controls
// (704), volt-var (705), volt-watt (706), frequency droop (711),
// watt-var (712) and the DER control model (715), then the end model.
// Each curve model holds three stored curves (control sets for 711): the
// first read-only, holding the settings in force, the others writable.
// Every function starts disabled. Follow makes the measurements obey
// them.
func DERInverter(o DEROptions) DER {
	if o.Manufacturer == "" {
		o.Manufacturer = "Example Solar"
	}
	if o.Model == "" {
		o.Model = "ES-10K-DER"
	}
	if o.Serial == "" {
		o.Serial = "DER0000001"
	}
	if o.WMax <= 0 {
		o.WMax = 10000
	}
	if o.VarMax <= 0 {
		o.VarMax = 0.44 * o.WMax
	}
	if o.VNom <= 0 {
		o.VNom = 230
	}
	im := NewImage(o.Base)
	d := DER{Image: im, At: map[int]uint16{}}
	add := func(id int, v map[string]any) {
		a, err := im.Add(id, v)
		if err != nil {
			panic(err) // the values below are fixed: a failure is a bug here
		}
		d.At[id] = a
	}
	w, q, v := int(o.WMax), int(o.VarMax), int(o.VNom*10)
	add(1, map[string]any{"Mn": o.Manufacturer, "Md": o.Model, "SN": o.Serial, "Vr": "1.0.0", "DA": 1})
	add(701, map[string]any{"ACType": sym(701, "ACType", "THREE_PHASE"), "St": sym(701, "St", "ON"),
		"InvSt": sym(701, "InvSt", "RUNNING"), "ConnSt": sym(701, "ConnSt", "CONNECTED"), "Alrm": 0, "DERMode": 1,
		"W": w, "VA": w, "Var": 0, "PF": 1000, "LNV": v, "LLV": int(float64(v) * 1.7320508), "Hz": 50000,
		"TotWhInj": 0, "TotWhAbs": 0, "ThrotPct": 100, "ThrotSrc": 0,
		"A_SF": -1, "V_SF": -1, "Hz_SF": -3, "W_SF": 0, "PF_SF": -3, "VA_SF": 0, "Var_SF": 0, "TotWh_SF": 0, "TotVarh_SF": 0, "Tmp_SF": 0})
	add(702, map[string]any{"WMaxRtg": w, "WMax": w, "VAMaxRtg": w, "VAMax": w, "VarMaxInjRtg": q, "VarMaxInj": q,
		"VarMaxAbsRtg": q, "VarMaxAbs": q, "VNomRtg": v, "VNom": v, "PFOvrExtRtg": 800, "PFUndExtRtg": 800,
		"W_SF": 0, "PF_SF": -3, "VA_SF": 0, "Var_SF": 0, "V_SF": -1, "A_SF": -1, "S_SF": 0})
	add(704, map[string]any{"PFWInjEna": 0, "PFWAbsEna": 0, "WMaxLimPctEna": 0, "WMaxLimPct": 1000, "WSetEna": 0,
		"WSetMod": sym(704, "WSetMod", "WATTS"), "WSet": 0, "WSetPct": 0, "VarSetEna": 0,
		"VarSetMod": sym(704, "VarSetMod", "VAR_MAX_PCT"), "VarSetPri": sym(704, "VarSetPri", "REACTIVE"), "VarSet": 0, "VarSetPct": 0,
		"PF_SF": -3, "WMaxLimPct_SF": -1, "WSet_SF": 0, "WSetPct_SF": -1, "VarSet_SF": 0, "VarSetPct_SF": -1,
		"PFWInj": map[string]any{"PF": 1000, "Ext": 0}, "PFWAbs": map[string]any{"PF": 1000, "Ext": 0}})
	ro, rw := sym(705, "ReadOnly", "R"), sym(705, "ReadOnly", "RW")
	curves := func(id int, first map[string]any, n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			c := map[string]any{"ReadOnly": rw}
			for k, x := range first {
				c[k] = x
			}
			if i == 0 {
				c["ReadOnly"] = ro
			}
			out[i] = c
		}
		return out
	}
	pts := func(xy ...[2]int) []map[string]any {
		out := make([]map[string]any, len(xy))
		for i, p := range xy {
			out[i] = map[string]any{"x": p[0], "y": p[1]}
		}
		return out
	}
	named := func(ps []map[string]any, x, y string) []map[string]any {
		for _, p := range ps {
			p[x], p[y] = p["x"], p["y"]
			delete(p, "x")
			delete(p, "y")
		}
		return ps
	}
	add(705, map[string]any{"Ena": 0, "AdptCrvReq": 0, "AdptCrvRslt": sym(705, "AdptCrvRslt", "COMPLETED"),
		"V_SF": 0, "DeptRef_SF": 0, "RspTms_SF": 0,
		"Crv": curves(705, map[string]any{"ActPt": 4, "DeptRef": sym(705, "DeptRef", "VAR_MAX_PCT"), "Pri": sym(705, "Pri", "REACTIVE"),
			"VRef": 100, "VRefAutoEna": 0, "RspTms": 5,
			"Pt": named(pts([2]int{92, 44}, [2]int{98, 0}, [2]int{102, 0}, [2]int{108, -44}), "V", "Var")}, 3)})
	add(706, map[string]any{"Ena": 0, "AdptCrvReq": 0, "AdptCrvRslt": sym(706, "AdptCrvRslt", "COMPLETED"),
		"V_SF": 0, "DeptRef_SF": 0, "RspTms_SF": 0,
		"Crv": curves(706, map[string]any{"ActPt": 2, "DeptRef": sym(706, "DeptRef", "W_MAX_PCT"), "RspTms": 10,
			"Pt": named(pts([2]int{106, 100}, [2]int{110, 0}), "V", "W")}, 3)})
	add(711, map[string]any{"Ena": 0, "AdptCtlReq": 0, "AdptCtlRslt": sym(711, "AdptCtlRslt", "COMPLETED"),
		"Db_SF": -3, "K_SF": -2, "RspTms_SF": 0,
		"Ctl": curves(711, map[string]any{"DbOf": 36, "DbUf": 36, "KOf": 5, "KUf": 5, "RspTms": 5, "PMin": 0}, 3)})
	add(712, map[string]any{"Ena": 0, "AdptCrvReq": 0, "AdptCrvRslt": sym(712, "AdptCrvRslt", "COMPLETED"),
		"W_SF": 0, "DeptRef_SF": 0,
		"Crv": curves(712, map[string]any{"ActPt": 3, "DeptRef": sym(712, "DeptRef", "VAR_MAX_PCT"), "Pri": sym(712, "Pri", "REACTIVE"),
			"Pt": named(pts([2]int{20, 0}, [2]int{50, 0}, [2]int{100, -44}), "W", "Var")}, 3)})
	add(715, map[string]any{"LocRemCtl": sym(715, "LocRemCtl", "REMOTE"), "DERHb": 0, "ControllerHb": 0, "AlarmReset": 0,
		"OpCtl": sym(715, "OpCtl", "START")})
	im.End()
	return d
}
