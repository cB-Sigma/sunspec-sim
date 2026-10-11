// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package interop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
)

// The peer: pysunspec2 at this version, in this image.
const (
	image      = "python:3.12.10-slim"
	pysunspec2 = "pysunspec2==1.3.6"
)

var (
	peerOnce sync.Once
	peerName string
	peerErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if peerName != "" {
		_ = exec.Command("docker", "container", "rm", "-f", peerName).Run()
	}
	os.Exit(code)
}

// needPeer starts the Python container once and installs pysunspec2 in it.
func needPeer(t *testing.T) string {
	t.Helper()
	if os.Getenv("SIGMA_SUNSPEC_INTEROP") != "1" {
		t.Skip("runs pysunspec2 in Docker; set SIGMA_SUNSPEC_INTEROP=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	peerOnce.Do(func() {
		td, err := filepath.Abs("testdata")
		if err != nil {
			peerErr = err
			return
		}
		name := fmt.Sprintf("sigma-sunspec-pysunspec2-client-%d", os.Getpid())
		out, err := exec.Command("docker", "run", "-d", "--name", name,
			"--add-host", "host.docker.internal:host-gateway",
			"-v", td+":/sigma:ro", image, "sleep", "3600").CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("docker run: %v: %s", err, out)
			return
		}
		peerName = name
		out, err = exec.Command("docker", "exec", name, "pip", "install", "--no-cache-dir",
			"--disable-pip-version-check", "--root-user-action=ignore", pysunspec2).CombinedOutput()
		if err != nil {
			peerErr = fmt.Errorf("pip install %s: %v: %s", pysunspec2, err, out)
		}
	})
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	return peerName
}

// listenAddr is where the simulator listens so that the container
// reaches it at host.docker.internal (see doc.go).
func listenAddr() string {
	if runtime.GOOS == "darwin" {
		return "127.0.0.1:0"
	}
	return "0.0.0.0:0"
}

func start(t *testing.T, im *sim.Image, units ...uint8) *sim.Device {
	t.Helper()
	d, err := sim.Start(im, listenAddr(), units...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

type report struct {
	Base      int    `json:"base"`
	ScanError string `json:"scan_error"`
	Models    []struct {
		ID     int            `json:"id"`
		Addr   int            `json:"addr"`
		Len    int            `json:"len"`
		Known  bool           `json:"known"`
		Error  string         `json:"error"`
		Values map[string]any `json:"values"`
	} `json:"models"`
	Writes map[string]struct {
		OK       bool   `json:"ok"`
		ReadBack any    `json:"readBack"`
		Error    string `json:"error"`
	} `json:"writes"`
}

// read runs pysunspec2 against the device, with writes ("model:point=value"
// in engineering units) after the scan.
func read(t *testing.T, d *sim.Device, unit uint8, writes ...string) report {
	t.Helper()
	name := needPeer(t)
	_, port, err := net.SplitHostPort(d.Addr())
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"exec", name, "python", "-I", "/sigma/pysunspec2_client.py",
		"--host", "host.docker.internal", "--port", port, "--unit", fmt.Sprint(unit)}
	for _, w := range writes {
		args = append(args, "--write", w)
	}
	var stdout, stderr bytes.Buffer
	c := exec.Command("docker", args...)
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	var r report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("pysunspec2 client: %v %v\n%s\n%s", runErr, err, stdout.String(), stderr.String())
	}
	if r.ScanError != "" || runErr != nil {
		t.Fatalf("pysunspec2 scan: %s %v\n%s", r.ScanError, runErr, stderr.String())
	}
	for _, m := range r.Models {
		if m.Known && m.Error != "" {
			t.Errorf("pysunspec2 reports model %d: %s", m.ID, m.Error)
		}
	}
	return r
}

// value finds "model.point" ("160.module.2.DCV": a repeating group's
// occurrence counts from 1) in the report; ok is false when absent.
func (r report) value(path string) (any, bool) {
	parts := strings.Split(path, ".")
	id, _ := strconv.Atoi(parts[0])
	for _, m := range r.Models {
		if m.ID != id {
			continue
		}
		var node any = m.Values
		for _, p := range parts[1:] {
			switch n := node.(type) {
			case map[string]any:
				v, ok := n[p]
				if !ok {
					return nil, false
				}
				node = v
			case []any:
				i, err := strconv.Atoi(p)
				if err != nil || i < 1 || i > len(n) {
					return nil, false
				}
				node = n[i-1]
			default:
				return nil, false
			}
		}
		return node, true
	}
	return nil, false
}

func (r report) ids() []int {
	var out []int
	for _, m := range r.Models {
		out = append(out, m.ID)
	}
	return out
}

func same(got, want any) bool {
	if g, ok := got.(float64); ok {
		var w float64
		switch x := want.(type) {
		case float64:
			w = x
		case int:
			w = float64(x)
		default:
			return false
		}
		return math.Abs(g-w) <= 1e-9*math.Max(1, math.Abs(w))
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

// expect checks values as pysunspec2 decoded them (scale factors
// applied), and that the not-implemented points came back as None.
func expect(t *testing.T, r report, want map[string]any, notImplemented ...string) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		got, ok := r.value(k)
		if !ok || !same(got, want[k]) {
			t.Errorf("pysunspec2 %s = %v (present %v), want %v", k, got, ok, want[k])
		}
	}
	for _, k := range notImplemented {
		if got, ok := r.value(k); !ok || got != nil {
			t.Errorf("pysunspec2 %s = %v (present %v), want not implemented (None)", k, got, ok)
		}
	}
	t.Logf("pysunspec2 agreed on %d values and %d not-implemented points", len(want), len(notImplemented))
}

func expectWrite(t *testing.T, r report, w string, readBack float64) {
	t.Helper()
	got, ok := r.Writes[w]
	if !ok || !got.OK || !same(got.ReadBack, readBack) {
		t.Errorf("pysunspec2 write %s: %+v, want read back %v", w, got, readBack)
	}
}

// The demo inverter (common, three-phase inverter, nameplate, MPPT with two
// inputs, DER controls 704 and 715, a vendor model) at 40000: pysunspec2
// finds the chain, decodes every model without a model error, agrees on
// the values and the not-implemented points, writes the output limit, the
// power factor and the start/stop control (sending function 6 for one
// register), and is refused on a read-only point.
func TestDemoInverter(t *testing.T) {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	d := start(t, demo.Image)
	r := read(t, d, 1,
		"704:WMaxLimPct=60", "704:WMaxLimPctEna=1", "704:PFWInj.PF=0.95", "704:PFWInj.Ext=1", "715:OpCtl=0", "103:W=5000")
	if r.Base != 40000 || !slices.Equal(r.ids(), []int{1, 103, 120, 160, 704, 715, 64900}) {
		t.Fatalf("pysunspec2 found base %d, models %v", r.Base, r.ids())
	}
	for _, m := range r.Models {
		want := map[int]uint16{1: demo.Common, 103: demo.Inverter, 120: demo.Nameplate, 160: demo.MPPT, 704: demo.Controls, 715: demo.DERCtl}[m.ID]
		if want != 0 && m.Addr != int(want)-2 {
			t.Errorf("model %d at %d, the simulator put it at %d", m.ID, m.Addr, want-2)
		}
		if m.ID == 64900 && (m.Known || m.Len != 3) {
			t.Errorf("vendor model: %+v", m)
		}
	}
	expect(t, r, map[string]any{
		"1.Mn": "Example Solar", "1.Md": "ES-10K-3P", "1.Vr": "1.0.0", "1.SN": "DEMO000001", "1.DA": 1,
		"103.A": 14.5, "103.AphA": 4.8, "103.AphB": 4.9, "103.AphC": 4.8, "103.PhVphA": 230.1, "103.PhVphB": 229.9,
		"103.PhVphC": 230.4, "103.W": 9800, "103.Hz": 50.01, "103.WH": 100000, "103.DCW": 10050, "103.TmpCab": 45.2,
		"103.St": 4, "103.Evt1": 0,
		"120.DERTyp": 4, "120.WRtg": 10000,
		"160.N": 2, "160.module.1.ID": 1, "160.module.1.IDStr": "PV1", "160.module.1.DCA": 8.35, "160.module.1.DCV": 610.5,
		"160.module.1.DCW": 5100, "160.module.2.IDStr": "PV2", "160.module.2.DCA": 8.12, "160.module.2.DCV": 608.5,
		"160.module.2.DCW": 4950,
		"704.WMaxLimPct":   100.0, "704.WMaxLimPctEna": 0, "704.PFWInjEna": 0, "704.PFWInj.PF": 1.0, "704.PFWInj.Ext": 0,
		"715.OpCtl": 1,
	}, "103.VA", "103.VAr", "103.PF", "103.TmpSnk", "103.DCA", "103.PPVphAB", "120.VARtg", "1.Opt", "704.WSet", "715.DERHb")

	expectWrite(t, r, "704:WMaxLimPct=60", 60)
	expectWrite(t, r, "704:WMaxLimPctEna=1", 1)
	expectWrite(t, r, "704:PFWInj.PF=0.95", 0.95)
	expectWrite(t, r, "704:PFWInj.Ext=1", 1)
	expectWrite(t, r, "715:OpCtl=0", 0)
	if w := r.Writes["103:W=5000"]; w.OK {
		t.Errorf("pysunspec2 wrote the read-only inverter.W: %+v", w)
	}
	for _, c := range []struct {
		addr uint16
		want uint16
	}{{demo.Controls + 13, 600}, {demo.Controls + 12, 1}, {demo.Controls + 57, 950}, {demo.Controls + 58, 1}, {demo.DERCtl + 6, 0}, {demo.Inverter + 12, 980}} {
		if got := d.Get(c.addr); got != c.want {
			t.Errorf("register %d = %d, want %d", c.addr, got, c.want)
		}
	}
	t.Logf("writes the simulator took: %v", d.WriteLog())
}

// The legacy demo (controls model 123) at 50000, answering on unit 126
// only: pysunspec2 addressing unit 126 finds it; its writes of model 123's
// limit and connection land.
func TestLegacyInverterUnit126(t *testing.T) {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 50000, Legacy: true, Serial: "LEGACY01"})
	d := start(t, demo.Image, 126)
	r := read(t, d, 126, "123:WMaxLimPct=75.5", "123:WMaxLim_Ena=1", "123:Conn=0")
	if r.Base != 50000 || !slices.Equal(r.ids(), []int{1, 103, 120, 160, 123, 64900}) {
		t.Fatalf("pysunspec2 found base %d, models %v", r.Base, r.ids())
	}
	expect(t, r, map[string]any{"1.SN": "LEGACY01", "103.W": 9800, "123.Conn": 1, "123.WMaxLimPct": 100.0},
		"123.OutPFSet", "123.VArPct_SF")
	expectWrite(t, r, "123:WMaxLimPct=75.5", 75.5)
	expectWrite(t, r, "123:WMaxLim_Ena=1", 1)
	expectWrite(t, r, "123:Conn=0", 0)
	if d.Get(demo.Controls+3) != 755 || d.Get(demo.Controls+7) != 1 || d.Get(demo.Controls+2) != 0 {
		t.Errorf("model 123 registers: %d %d %d", d.Get(demo.Controls+3), d.Get(demo.Controls+7), d.Get(demo.Controls+2))
	}
}

// layout sets points of a model's data by name, at the offsets of this
// module's tables (pysunspec2 decodes them with its own definitions).
type layout struct {
	def  *sunspec.Model
	data []uint16
}

func newLayout(t *testing.T, id int) *layout {
	t.Helper()
	def := sunspec.Lookup(id)
	if def == nil || def.Len == 0 {
		t.Fatalf("model %d: no fixed-length definition", id)
	}
	// Every point starts at its not-implemented value.
	return &layout{def: def, data: sim.NotImplemented(id)}
}

func (l *layout) set(t *testing.T, name string, words ...uint16) *layout {
	t.Helper()
	for _, p := range l.def.Points {
		if p.Name == name {
			if len(words) != p.Size {
				t.Fatalf("%s.%s takes %d registers", l.def.Name, name, p.Size)
			}
			copy(l.data[p.Offset:], words)
			return l
		}
	}
	t.Fatalf("model %d has no point %s", l.def.ID, name)
	return l
}

func i16(v int) uint16 { return uint16(int16(v)) }

func u32(v uint32) []uint16 { return []uint16{uint16(v >> 16), uint16(v)} }

func u64(v uint64) []uint16 {
	return []uint16{uint16(v >> 48), uint16(v >> 32), uint16(v >> 16), uint16(v)}
}

func f32(v float32) []uint16 { return u32(math.Float32bits(v)) }

// More models at address 0 (pysunspec2 tries 40000, then 0): single-phase
// integer and float inverters, a meter, the DER measurement, capacity and
// storage models and the status model, laid out at this module's offsets:
// negative integers, 32- and 64-bit values, float32 and its
// not-implemented NaN, strings.
func TestMoreModelsAtZero(t *testing.T) {
	im := sim.NewImage(0)
	im.Model(1, append(append(append(append(append(sim.Str("Example Power", 16), sim.Str("EP-5K", 16)...),
		sim.Str("", 8)...), sim.Str("2.1", 8)...), sim.Str("X-0042", 16)...), 1, 0))
	l := newLayout(t, 101)
	l.set(t, "A", 52).set(t, "A_SF", i16(-1)).set(t, "PhVphA", 2405).set(t, "V_SF", i16(-1)).set(t, "W", i16(-150)).set(t, "W_SF", 0).
		set(t, "Hz", 4998).set(t, "Hz_SF", i16(-2)).set(t, "PF", i16(-95)).set(t, "PF_SF", i16(-2)).
		set(t, "WH", u32(123456789)...).set(t, "WH_SF", 0).set(t, "St", 2)
	im.Model(101, l.data)
	l = newLayout(t, 111)
	l.set(t, "W", f32(1234.5)...).set(t, "Hz", f32(50)...).set(t, "PhVphA", f32(231.25)...).set(t, "PF", f32(-0.75)...).set(t, "St", 4)
	im.Model(111, l.data)
	l = newLayout(t, 201)
	l.set(t, "A", 105).set(t, "A_SF", i16(-1)).set(t, "PhV", 2302).set(t, "V_SF", i16(-1)).set(t, "Hz", 5000).set(t, "Hz_SF", i16(-2)).
		set(t, "W", i16(-2500)).set(t, "W_SF", 0).set(t, "PF", 98).set(t, "PF_SF", i16(-2)).
		set(t, "TotWhExp", u32(987654)...).set(t, "TotWhImp", u32(4321)...).set(t, "TotWh_SF", 0)
	im.Model(201, l.data)
	l = newLayout(t, 701)
	l.set(t, "W", 5000).set(t, "W_SF", 0).set(t, "Hz", u32(50010)...).set(t, "Hz_SF", i16(-3)).
		set(t, "TotWhInj", u64(12345678901)...).set(t, "TotWh_SF", 0).set(t, "LNV", 2301).set(t, "V_SF", i16(-1)).
		set(t, "TmpCab", 41).set(t, "Tmp_SF", 0).set(t, "ThrotPct", 80).set(t, "ConnSt", 1)
	im.Model(701, l.data)
	l = newLayout(t, 713)
	l.set(t, "WHRtg", 10000).set(t, "WH_SF", 0).set(t, "SoC", 655).set(t, "SoH", 980).set(t, "Pct_SF", i16(-1))
	im.Model(713, l.data)
	l = newLayout(t, 122)
	l.set(t, "PVConn", 0b11).set(t, "ActWh", u64(5000000000)...).set(t, "TmSrc", sim.Str("NTP", 4)...).set(t, "Tms", u32(123456)...)
	im.Model(122, l.data)
	im.End()
	d := start(t, im)
	r := read(t, d, 1)
	if r.Base != 0 || !slices.Equal(r.ids(), []int{1, 101, 111, 201, 701, 713, 122}) {
		t.Fatalf("pysunspec2 found base %d, models %v", r.Base, r.ids())
	}
	expect(t, r, map[string]any{
		"1.Mn": "Example Power", "1.Vr": "2.1",
		"101.A": 5.2, "101.PhVphA": 240.5, "101.W": -150, "101.Hz": 49.98, "101.PF": -0.95, "101.WH": 123456789, "101.St": 2,
		"111.W": 1234.5, "111.Hz": 50.0, "111.PhVphA": 231.25, "111.PF": -0.75, "111.St": 4,
		"201.A": 10.5, "201.PhV": 230.2, "201.Hz": 50.0, "201.W": -2500, "201.PF": 0.98, "201.TotWhExp": 987654, "201.TotWhImp": 4321,
		"701.W": 5000, "701.Hz": 50.01, "701.TotWhInj": 12345678901, "701.LNV": 230.1, "701.TmpCab": 41, "701.ThrotPct": 80, "701.ConnSt": 1,
		"713.WHRtg": 10000, "713.SoC": 65.5, "713.SoH": 98.0,
		"122.PVConn": 3, "122.ActWh": 5000000000, "122.TmSrc": "NTP", "122.Tms": 123456,
	}, "101.VA", "101.TmpCab", "111.A", "111.VA", "111.WH", "201.VA", "201.TotVAhExp", "701.VA", "701.TotVarhInj",
		"713.WHAvail", "122.ActVAh", "122.WAval")
}
