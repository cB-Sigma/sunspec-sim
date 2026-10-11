// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec/device"
)

// Grid is the simulated grid at the inverter's terminals and the power
// its source could give.
type Grid struct {
	// V is the line-to-neutral voltage in V (0: the nominal voltage).
	V float64
	// Hz is the frequency (0: the nominal frequency).
	Hz float64
	// Avail is the active power the source could give, in W (0: the
	// rating).
	Avail float64
}

// FollowOptions shapes Follow.
type FollowOptions struct {
	// Grid is the grid to start with.
	Grid Grid
	// HzNom is the nominal frequency (0: 50 Hz).
	HzNom float64
}

// Output is what the simulated inverter puts out, in engineering units.
type Output struct {
	W, Var, VA, PF float64
	// Functions names the functions that shaped it (the throttle source
	// names of model 701: MAX_W, FIXED_W, VOLT_WATT, FREQ_WATT,
	// FIXED_PF, FIXED_VAR, VOLT_VAR, WATT_VAR).
	Functions []string
	// Stopped is true after model 715's STOP.
	Stopped bool
}

// Follower makes a simulated inverter's measurements follow its DER
// settings and adopted curves. Rules, in our own words after IEEE
// 1547-2018 and the SunSpec 7xx models:
//
//   - Adopting: a write of n to a curve model's adopt request (705 to 712)
//     checks stored curve n (it exists; its active point count is within
//     the number of points; its x values rise), copies it over the first
//     curve (the settings in force, whose read-only flag stays), sets the
//     adopt result to COMPLETED (or FAILED, leaving the first curve) and
//     the request back to 0.
//   - Active power: the available power, capped by 704's limit (MAX_W) and
//     setpoint (FIXED_W), by volt-watt (706) at the grid voltage, and by
//     frequency droop above the dead band (711: the excess frequency over
//     the nominal times the droop gain, as a share of WMax, never below
//     PMin). Under-frequency response cannot add power above what is
//     available, so it has no effect here.
//   - Reactive power, first match: 704's fixed power factor (FIXED_PF),
//     704's reactive setpoint (FIXED_VAR), volt-var (705) at the grid
//     voltage, watt-var (712) at the output power; limited to VarMax.
//   - 715's STOP: no output.
//
// The results go to 701 (W, VA, Var, PF, A, LNV, LLV, Hz, ThrotPct,
// ThrotSrc, St, InvSt) and, when present, 103. Changes are immediate: the
// response times, ramps, reversion timers and the trip curves (707 to
// 710, which are adopted but not acted on) are not simulated. The power
// factor is |W|/VA, negative when the inverter absorbs reactive power.
type Follower struct {
	d      *Device
	mu     sync.Mutex
	grid   Grid
	hzNom  float64
	models map[int]sunspec.Instance
	out    Output
}

// Follow makes d's outputs follow its settings (see Follower). The map
// must have the DER AC measurement model (701). It takes the device's
// write hook (SetOnWrite).
func Follow(d *Device, o FollowOptions) (*Follower, error) {
	f := &Follower{d: d, grid: o.Grid, hzNom: o.HzNom, models: map[int]sunspec.Instance{}}
	if f.hzNom <= 0 {
		f.hzNom = 50
	}
	var err error
	d.Update(func(im *Image) {
		var m *sunspec.Map
		if m, err = sunspec.Walk(im, im.Base); err != nil {
			return
		}
		for _, inst := range m.Models {
			if _, seen := f.models[inst.ID]; !seen && inst.Def != nil {
				f.models[inst.ID] = inst
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("sunspec sim: following: %w", err)
	}
	if _, ok := f.models[701]; !ok {
		return nil, errors.New("sunspec sim: following needs the DER AC measurement model (701)")
	}
	d.SetOnWrite(f.onWrite)
	d.Update(f.recompute)
	return f, nil
}

// SetGrid changes the simulated grid; the outputs follow at once.
func (f *Follower) SetGrid(g Grid) {
	f.mu.Lock()
	f.grid = g
	f.mu.Unlock()
	f.d.Update(f.recompute)
}

// Output is the last output computed.
func (f *Follower) Output() Output {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.out
	out.Functions = append([]string(nil), f.out.Functions...)
	return out
}

// mv is one model's registers in the image, decoded.
type mv struct {
	inst   sunspec.Instance
	regs   []uint16
	vals   map[string]sunspec.Value
	places map[string]sunspec.Located
}

func (f *Follower) load(im *Image, id int) *mv {
	inst, ok := f.models[id]
	if !ok {
		return nil
	}
	regs, err := im.ReadHolding(inst.Addr, uint16(inst.L))
	if err != nil {
		return nil
	}
	m := &mv{inst: inst, regs: regs, vals: map[string]sunspec.Value{}, places: map[string]sunspec.Located{}}
	for _, v := range sunspec.Decode(inst, regs) {
		m.vals[v.Path] = v
	}
	for _, l := range sunspec.Layout(inst, regs) {
		m.places[l.Path] = l
	}
	return m
}

func (m *mv) num(path string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	f, ok := m.vals[path].V.(float64)
	return f, ok
}

func (m *mv) sym(path string) string {
	if m == nil {
		return ""
	}
	s, _ := m.vals[path].V.(string)
	return s
}

func (m *mv) enabled(path string) bool { return m.sym(path) == "ENABLED" }

// put sets a point from an engineering value, through its scale factor;
// a point whose scale factor is not implemented, or a value that does
// not fit, is left as it is.
func (m *mv) put(im *Image, path string, v float64) {
	if m == nil {
		return
	}
	l, ok := m.places[path]
	if !ok {
		return
	}
	var words []uint16
	var err error
	if strings.HasPrefix(l.Point.Type, "float") {
		words, err = sunspec.EncodePoint(l.Point, v)
	} else {
		sf, scaled, usable := l.Scale(m.regs)
		if !usable {
			return
		}
		if scaled {
			v /= math.Pow10(sf)
		}
		words, err = sunspec.EncodePoint(l.Point, int64(math.Round(v)))
	}
	if err != nil {
		return
	}
	im.Set(m.inst.Addr+uint16(l.Offset), words...)
	copy(m.regs[l.Offset:], words)
}

func (m *mv) putRaw(im *Image, path string, raw int64) {
	if m == nil {
		return
	}
	l, ok := m.places[path]
	if !ok {
		return
	}
	if words, err := sunspec.EncodePoint(l.Point, raw); err == nil {
		im.Set(m.inst.Addr+uint16(l.Offset), words...)
		copy(m.regs[l.Offset:], words)
	}
}

func (m *mv) putSym(im *Image, path, name string) {
	if m == nil {
		return
	}
	if l, ok := m.places[path]; ok {
		if raw, ok := l.Point.Symbols[name]; ok {
			m.putRaw(im, path, raw)
		}
	}
}

// curve reads the first (in-force) curve's active points of a model:
// group, x and y point names.
func (m *mv) curve(group, x, y string) [][2]float64 {
	act, ok := m.num(group + ".1.ActPt")
	if !ok {
		return nil
	}
	var out [][2]float64
	for k := 1; k <= int(act); k++ {
		xv, ok1 := m.num(fmt.Sprintf("%s.1.Pt.%d.%s", group, k, x))
		yv, ok2 := m.num(fmt.Sprintf("%s.1.Pt.%d.%s", group, k, y))
		if !ok1 || !ok2 {
			return nil
		}
		out = append(out, [2]float64{xv, yv})
	}
	return out
}

// interp reads a piecewise-linear curve at x, flat beyond its ends.
func interp(pts [][2]float64, x float64) (float64, bool) {
	if len(pts) == 0 {
		return 0, false
	}
	if x <= pts[0][0] {
		return pts[0][1], true
	}
	for i := 1; i < len(pts); i++ {
		if x <= pts[i][0] {
			a, b := pts[i-1], pts[i]
			if b[0] == a[0] {
				return b[1], true
			}
			return a[1] + (b[1]-a[1])*(x-a[0])/(b[0]-a[0]), true
		}
	}
	return pts[len(pts)-1][1], true
}

var adoptModels = map[int][2]string{705: {"AdptCrvReq", "AdptCrvRslt"}, 706: {"AdptCrvReq", "AdptCrvRslt"},
	707: {"AdptCrvReq", "AdptCrvRslt"}, 708: {"AdptCrvReq", "AdptCrvRslt"}, 709: {"AdptCrvReq", "AdptCrvRslt"},
	710: {"AdptCrvReq", "AdptCrvRslt"}, 711: {"AdptCtlReq", "AdptCtlRslt"}, 712: {"AdptCrvReq", "AdptCrvRslt"}}

func (f *Follower) onWrite(im *Image, w device.Request) {
	for id, pts := range adoptModels {
		inst, ok := f.models[id]
		if !ok || int(w.Addr)+int(w.Count) <= int(inst.Addr) || int(w.Addr) >= int(inst.Addr)+inst.L {
			continue
		}
		m := f.load(im, id)
		l, ok := m.places[pts[0]]
		if !ok || int(w.Addr) > int(inst.Addr)+l.Offset || int(w.Addr)+int(w.Count) <= int(inst.Addr)+l.Offset {
			continue
		}
		f.adopt(im, m, pts[0], pts[1])
	}
	f.recompute(im)
}

// adopt carries out a model's adopt request.
func (f *Follower) adopt(im *Image, m *mv, req, rslt string) {
	n, ok := m.num(req)
	defer m.putRaw(im, req, 0)
	if !ok || n == 0 {
		return
	}
	group := m.inst.Def.Groups[0].Name
	prefix := fmt.Sprintf("%s.%d.", group, int(n))
	if !f.valid(m, prefix) {
		m.putSym(im, rslt, "FAILED")
		return
	}
	if n != 1 {
		from, to := span(m, prefix), span(m, group+".1.")
		if from[1]-from[0] != to[1]-to[0] {
			m.putSym(im, rslt, "FAILED")
			return
		}
		ro := m.places[group+".1.ReadOnly"]
		keep := m.regs[ro.Offset]
		data := append([]uint16(nil), m.regs[from[0]:from[1]]...)
		im.Set(m.inst.Addr+uint16(to[0]), data...)
		if _, ok := m.places[group+".1.ReadOnly"]; ok {
			im.Set(m.inst.Addr+uint16(ro.Offset), keep)
		}
		copy(m.regs[to[0]:], data)
		m.regs[ro.Offset] = keep
	}
	m.putSym(im, rslt, "COMPLETED")
}

// span is the registers an occurrence's points take, [first, end).
func span(m *mv, prefix string) [2]int {
	out := [2]int{math.MaxInt, 0}
	for path, l := range m.places {
		if strings.HasPrefix(path, prefix) {
			out[0] = min(out[0], l.Offset)
			out[1] = max(out[1], l.Offset+l.Point.Size)
		}
	}
	return out
}

// valid checks a stored curve: it exists, and each of its point lists
// has an active count within the points stored and x values that rise.
func (f *Follower) valid(m *mv, prefix string) bool {
	if _, ok := m.places[prefix+"ReadOnly"]; !ok {
		return false
	}
	var acts []string
	for path := range m.places {
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, "ActPt") {
			acts = append(acts, strings.TrimSuffix(path, "ActPt"))
		}
	}
	sort.Strings(acts)
	for _, p := range acts {
		act, ok := m.num(p + "ActPt")
		if !ok || act < 1 {
			return false
		}
		// The x point is the first point of each Pt occurrence.
		var xName string
		first := math.MaxInt
		for path, l := range m.places {
			if strings.HasPrefix(path, p+"Pt.1.") && l.Offset < first {
				first, xName = l.Offset, strings.TrimPrefix(path, p+"Pt.1.")
			}
		}
		if xName == "" {
			return false
		}
		last := math.Inf(-1)
		for k := 1; k <= int(act); k++ {
			x, ok := m.num(fmt.Sprintf("%sPt.%d.%s", p, k, xName))
			if !ok || x <= last {
				return false
			}
			last = x
		}
	}
	return true
}

func (f *Follower) recompute(im *Image) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cap702 := f.load(im, 702)
	wmax, ok := cap702.num("WMax")
	if !ok {
		if wmax, ok = cap702.num("WMaxRtg"); !ok {
			wmax = 10000
		}
	}
	varmax, ok := cap702.num("VarMaxInj")
	if !ok {
		if varmax, ok = cap702.num("VarMaxInjRtg"); !ok {
			varmax = 0.44 * wmax
		}
	}
	vamax, ok := cap702.num("VAMax")
	if !ok {
		vamax = wmax
	}
	vnom, ok := cap702.num("VNom")
	if !ok {
		if vnom, ok = cap702.num("VNomRtg"); !ok {
			vnom = 230
		}
	}
	g := f.grid
	if g.V <= 0 {
		g.V = vnom
	}
	if g.Hz <= 0 {
		g.Hz = f.hzNom
	}
	if g.Avail <= 0 {
		g.Avail = wmax
	}
	vpct := g.V / vnom * 100
	out := Output{W: g.Avail}
	use := func(name string) { out.Functions = append(out.Functions, name) }
	capW := func(limit float64, name string) {
		if out.W > limit {
			out.W = limit
			use(name)
		}
	}

	ctl := f.load(im, 715)
	out.Stopped = ctl.sym("OpCtl") == "STOP"

	ac := f.load(im, 704)
	if ac.enabled("WMaxLimPctEna") {
		if pct, ok := ac.num("WMaxLimPct"); ok {
			capW(pct/100*wmax, "MAX_W")
		}
	}
	if ac.enabled("WSetEna") {
		if ac.sym("WSetMod") == "W_MAX_PCT" {
			if pct, ok := ac.num("WSetPct"); ok {
				capW(pct/100*wmax, "FIXED_W")
			}
		} else if set, ok := ac.num("WSet"); ok {
			capW(set, "FIXED_W")
		}
	}
	if vw := f.load(im, 706); vw.enabled("Ena") {
		if y, ok := interp(vw.curve("Crv", "V", "W"), vpct); ok {
			ref := wmax
			if vw.sym("Crv.1.DeptRef") == "W_AVAL_PCT" {
				ref = g.Avail
			}
			capW(y/100*ref, "VOLT_WATT")
		}
	}
	if fd := f.load(im, 711); fd.enabled("Ena") {
		db, ok1 := fd.num("Ctl.1.DbOf")
		k, ok2 := fd.num("Ctl.1.KOf")
		if ok1 && ok2 && k > 0 && g.Hz > f.hzNom+db {
			pmin, _ := fd.num("Ctl.1.PMin")
			limit := max(g.Avail-(g.Hz-f.hzNom-db)/(f.hzNom*k)*wmax, pmin/100*wmax)
			capW(limit, "FREQ_WATT")
		}
	}
	out.W = max(out.W, 0)

	ref := func(m *mv, path string) float64 {
		switch m.sym(path) {
		case "W_MAX_PCT":
			return wmax
		case "VA_MAX_PCT":
			return vamax
		}
		return varmax
	}
	switch {
	case ac.enabled("PFWInjEna"):
		if pf, ok := ac.num("PFWInj.PF"); ok && pf > 0 && pf <= 1 {
			out.Var = out.W * math.Tan(math.Acos(pf))
			if ac.sym("PFWInj.Ext") == "UNDER_EXCITED" {
				out.Var = -out.Var
			}
			use("FIXED_PF")
		}
	case ac.enabled("VarSetEna"):
		mode := ac.sym("VarSetMod")
		if mode == "VARS" {
			out.Var, _ = ac.num("VarSet")
		} else if pct, ok := ac.num("VarSetPct"); ok {
			r := varmax
			switch mode {
			case "W_MAX_PCT":
				r = wmax
			case "VA_MAX_PCT":
				r = vamax
			}
			out.Var = pct / 100 * r
		}
		use("FIXED_VAR")
	default:
		if vv := f.load(im, 705); vv.enabled("Ena") {
			if y, ok := interp(vv.curve("Crv", "V", "Var"), vpct); ok {
				out.Var = y / 100 * ref(vv, "Crv.1.DeptRef")
				use("VOLT_VAR")
			}
		} else if wv := f.load(im, 712); wv.enabled("Ena") {
			if y, ok := interp(wv.curve("Crv", "W", "Var"), out.W/wmax*100); ok {
				out.Var = y / 100 * ref(wv, "Crv.1.DeptRef")
				use("WATT_VAR")
			}
		}
	}
	out.Var = math.Max(-varmax, math.Min(varmax, out.Var))
	if out.Stopped {
		out.W, out.Var, out.Functions = 0, 0, nil
	}
	out.VA = math.Hypot(out.W, out.Var)
	out.PF = 1
	if out.VA > 0 {
		out.PF = out.W / out.VA
		if out.Var < 0 {
			out.PF = -out.PF
		}
	}
	f.out = out

	m := f.load(im, 701)
	phases := 1.0
	if m.sym("ACType") == "THREE_PHASE" {
		phases = 3
	}
	m.put(im, "W", out.W)
	m.put(im, "VA", out.VA)
	m.put(im, "Var", out.Var)
	m.put(im, "PF", out.PF)
	m.put(im, "A", out.VA/g.V/phases)
	m.put(im, "LNV", g.V)
	if phases == 3 {
		m.put(im, "LLV", g.V*math.Sqrt(3))
	}
	m.put(im, "Hz", g.Hz)
	m.put(im, "ThrotPct", out.W/g.Avail*100)
	if l, ok := m.places["ThrotSrc"]; ok {
		var bits int64
		for _, name := range out.Functions {
			if b, ok := l.Point.Symbols[name]; ok {
				bits |= 1 << b
			}
		}
		m.putRaw(im, "ThrotSrc", bits)
	}
	switch {
	case out.Stopped:
		m.putSym(im, "St", "OFF")
		m.putSym(im, "InvSt", "OFF")
	case out.W < g.Avail-0.5:
		m.putSym(im, "St", "ON")
		m.putSym(im, "InvSt", "THROTTLED")
	default:
		m.putSym(im, "St", "ON")
		m.putSym(im, "InvSt", "RUNNING")
	}
	if inv := f.load(im, 103); inv != nil {
		inv.put(im, "W", out.W)
		inv.put(im, "VA", out.VA)
		inv.put(im, "VAr", out.Var)
		inv.put(im, "PF", out.PF*100)
		inv.put(im, "PhVphA", g.V)
		inv.put(im, "Hz", g.Hz)
	}
}
