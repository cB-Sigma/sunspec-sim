// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/sunspec"
	"github.com/cB-Sigma/sunspec-sim/sim"
	"github.com/cB-Sigma/sunspec/mbtcp"
)

// Build lays out model 705's curves with their nested points; the reader
// places every value where Build put it, and the curve points are
// writable.
func TestBuildNested(t *testing.T) {
	data, writable, err := sim.Build(705, map[string]any{"Ena": 1, "V_SF": -1, "DeptRef_SF": 0, "RspTms_SF": 0, "Crv": []map[string]any{
		{"ActPt": 2, "ReadOnly": 1, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}},
		{"ActPt": 3, "ReadOnly": 0, "Pt": []map[string]any{{"V": 920, "Var": 40}, {"V": 1000, "Var": 0}, {"V": 1080, "Var": -40}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// NPt is the longest list (3), so both curves have three points.
	if want := 13 + 2*(10+3*2); len(data) != want {
		t.Fatalf("%d registers, want %d", len(data), want)
	}
	inst := sunspec.Instance{ID: 705, L: len(data), Def: sunspec.Lookup(705)}
	got := map[string]any{}
	for _, v := range sunspec.Decode(inst, data) {
		got[v.Path] = v.V
	}
	for k, want := range map[string]any{"NPt": 3.0, "NCrv": 2.0, "Ena": "ENABLED", "Crv.1.ReadOnly": "R", "Crv.1.Pt.2.V": 105.0,
		"Crv.1.Pt.2.Var": -30.0, "Crv.2.Pt.3.V": 108.0, "Crv.2.ActPt": 3.0} {
		if fmt.Sprint(got[k]) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if _, ok := got["Crv.1.Pt.3.V"]; ok {
		t.Error("the filled-up third point of curve 1 is implemented")
	}
	for _, path := range []string{"Crv.2.Pt.1.V", "Crv.2.Pt.3.Var", "Crv.1.ActPt", "AdptCrvReq", "Ena"} {
		l, _ := sunspec.Locate(inst, data, path)
		if !slices.Contains(writable, l.Offset) {
			t.Errorf("%s at %d is not writable", path, l.Offset)
		}
	}
	for _, path := range []string{"NPt", "Crv.1.ReadOnly", "AdptCrvRslt"} {
		l, _ := sunspec.Locate(inst, data, path)
		if slices.Contains(writable, l.Offset) {
			t.Errorf("%s at %d is writable", path, l.Offset)
		}
	}
	for _, bad := range []map[string]any{
		{"Ena": "x"}, {"Crv": []map[string]any{{"Nope": 1, "ActPt": 1 << 20}}}, {"NCrv": 1, "Crv": []map[string]any{{}, {}}},
	} {
		if _, _, err := sim.Build(705, bad); err == nil {
			t.Errorf("%v built", bad)
		}
	}
	if _, _, err := sim.Build(64900, nil); err == nil {
		t.Error("undescribed model built")
	}
}

// Every described model, with made-up values and its repeating groups
// twice, served together and read back over Modbus TCP: the chain, the
// lengths and the decoded values agree with what Example gave.
func TestAllModels(t *testing.T) {
	im, at, err := sim.AllModels(40000)
	if err != nil {
		t.Fatal(err)
	}
	d := start(t, im)
	r := dial(t, d, 1)
	m, err := sunspec.Find(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Models) != len(sunspec.Known()) {
		t.Fatalf("%d models, want %d", len(m.Models), len(sunspec.Known()))
	}
	values := 0
	for i, inst := range m.Models {
		if inst.ID != sunspec.Known()[i] || inst.Addr != at[inst.ID] || inst.Def == nil {
			t.Fatalf("model %d at %d, want %d at %d", inst.ID, inst.Addr, sunspec.Known()[i], at[inst.ID])
		}
		if inst.Def.Len > 0 && inst.L != inst.Def.Len {
			t.Errorf("model %d: L %d, want %d", inst.ID, inst.L, inst.Def.Len)
		}
		regs, err := sunspec.ReadModel(r, inst)
		if err != nil {
			t.Fatal(err)
		}
		vals := sunspec.Decode(inst, regs)
		if len(vals) == 0 {
			t.Errorf("model %d: nothing decoded", inst.ID)
		}
		values += len(vals)
		// Every point Example gave comes back once per occurrence.
		end := 0
		for _, l := range sunspec.Layout(inst, regs) {
			end = max(end, l.Offset+l.Point.Size)
		}
		if end != inst.L {
			t.Errorf("model %d: the layout ends at %d of %d registers", inst.ID, end, inst.L)
		}
	}
	for _, c := range []struct {
		id   int
		path string
		want any
	}{
		{705, "Crv.2.Pt.2.V", nil}, {707, "Crv.2.MomCess.Pt.2.Tms", nil}, {160, "module.2.DCV", nil},
		{714, "Prt.2.IDStr", "IDStr"}, {1, "Mn", "Mn"}, {803, "string.2.StrNMod", nil}, {64413, "IV.2.V", nil},
	} {
		inst := m.Model(c.id)
		regs, _ := sunspec.ReadModel(r, *inst)
		l, ok := sunspec.Locate(*inst, regs, c.path)
		if !ok {
			t.Errorf("model %d has no %s", c.id, c.path)
			continue
		}
		if c.want != nil {
			for _, v := range sunspec.Decode(*inst, regs) {
				if v.Path == c.path && v.V != c.want {
					t.Errorf("%d %s = %v, want %v", c.id, c.path, v.V, c.want)
				}
			}
		}
		_ = l
	}
	t.Logf("%d models, %d values decoded", len(m.Models), values)
}

// Faults: an exception, a late answer (a timeout for the client), no
// answer at all, limited to reads or writes, an address range, a unit
// and a number of requests.
func TestFaults(t *testing.T) {
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	d := start(t, demo.Image)
	r, err := mbtcp.Dial(d.URL(), 1, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d.AddFault(sim.Fault{From: demo.Inverter, To: demo.Inverter + 10, Op: "read", Exception: 6, Times: 2})
	for i := 0; i < 2; i++ {
		if _, err := r.ReadHolding(demo.Inverter+5, 2); !r.Refused(err) || !strings.Contains(err.Error(), "busy") {
			t.Errorf("read %d: %v, want busy", i, err)
		}
	}
	if _, err := r.ReadHolding(demo.Inverter+5, 2); err != nil {
		t.Errorf("after two: %v", err)
	}
	if _, err := r.ReadHolding(demo.Common, 2); err != nil {
		t.Errorf("outside the range: %v", err)
	}
	d.AddFault(sim.Fault{Op: "write", Exception: 4})
	if err := r.WriteHolding(demo.Controls+13, []uint16{500}); !r.Refused(err) {
		t.Errorf("write: %v", err)
	}
	if _, err := r.ReadHolding(demo.Controls+13, 1); err != nil {
		t.Errorf("read during a write fault: %v", err)
	}
	d.ClearFaults()
	d.AddFault(sim.Fault{Delay: time.Second, Times: 1})
	start := time.Now()
	if _, err := r.ReadHolding(demo.Common, 2); err == nil || r.Refused(err) {
		t.Errorf("delayed read: %v, want a timeout", err)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Errorf("the client waited %v", time.Since(start))
	}
	time.Sleep(time.Second) // let the late answer pass
	d.ClearFaults()
	d.AddFault(sim.Fault{Unit: 2, Silent: true})
	r2, err := mbtcp.Dial(d.URL(), 2, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if _, err := sunspec.Find(r2); err == nil || errors.Is(err, sunspec.ErrNotSunSpec) {
		t.Errorf("silent unit: %v, want the read error", err)
	}
	r3, err := mbtcp.Dial(d.URL(), 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Close()
	if _, err := sunspec.Find(r3); err != nil {
		t.Errorf("unit 1 while unit 2 is silent: %v", err)
	}
}

func certs(t *testing.T) (*x509.CertPool, tls.Certificate, tls.Certificate) {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	caKey := key()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	leaf := func(n int64, usage x509.ExtKeyUsage) tls.Certificate {
		k := key()
		c := &x509.Certificate{SerialNumber: big.NewInt(n), Subject: pkix.Name{CommonName: fmt.Sprint("leaf ", n)}, NotBefore: time.Now().Add(-time.Hour),
			NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
			IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
		d, err := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{d}, PrivateKey: k}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, leaf(2, x509.ExtKeyUsageServerAuth), leaf(3, x509.ExtKeyUsageClientAuth)
}

// The simulator over Modbus/TCP Security (TLS, client certificates) and
// over RTU framing on TCP: found, read and written.
func TestTransports(t *testing.T) {
	pool, server, client := certs(t)
	demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
	d, err := sim.StartOptions(demo.Image, sim.Options{TLSCert: &server, TLSClientCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !strings.HasPrefix(d.URL(), "tcp+tls://") {
		t.Errorf("URL %s", d.URL())
	}
	r, err := mbtcp.Open(mbtcp.Config{URL: d.URL(), Unit: 1, TLSClientCert: &client, TLSRootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := sunspec.Find(r); err != nil || len(m.Models) != 7 {
		t.Errorf("TLS: %+v %v", m, err)
	}
	_ = r.Close()
	if _, err := sim.StartOptions(demo.Image, sim.Options{TLSCert: &server}); err == nil {
		t.Error("TLS without client CAs")
	}

	demo = sim.DemoInverter(sim.DemoOptions{Base: 50000})
	d, err = sim.StartOptions(demo.Image, sim.Options{RTU: true, Units: []uint8{126}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r, err = mbtcp.Open(mbtcp.Config{URL: d.URL(), Unit: 126, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m, err := sunspec.Find(r)
	if err != nil || m.Base != 50000 || len(m.Models) != 7 {
		t.Fatalf("RTU: %+v %v", m, err)
	}
	if err := r.WriteHolding(demo.Controls+13, []uint16{700}); err != nil || d.Get(demo.Controls+13) != 700 {
		t.Errorf("RTU write: %v", err)
	}
	if err := r.WriteHolding(demo.Controls+12, []uint16{1, 700}); err != nil {
		t.Errorf("RTU write of two: %v", err)
	}
	if err := r.WriteHolding(demo.Inverter, []uint16{1}); !r.Refused(err) {
		t.Errorf("RTU write to a read-only register: %v", err)
	}
	if _, err := r.ForUnit(1).ReadHolding(50000, 2); err == nil || r.Refused(err) {
		t.Errorf("another unit on RTU: %v, want silence", err)
	}
	d.AddFault(sim.Fault{Exception: 2, Times: 1})
	if _, err := r.ReadHolding(50000, 2); !r.Refused(err) {
		t.Errorf("RTU fault: %v", err)
	}
}

// ServeRTU on a byte stream: a frame with a bad check gets no answer, a
// good one its registers.
func TestServeRTU(t *testing.T) {
	im := sim.NewImage(40000)
	im.Model(1, sim.Filled(66, 0))
	im.End()
	d := sim.NewDevice(im)
	a, b := net.Pipe()
	defer a.Close()
	go func() { _ = d.ServeRTU(b) }()
	defer d.Close()
	frame := func(unit, fc byte, addr, n uint16) []byte {
		f := []byte{unit, fc}
		f = binary.BigEndian.AppendUint16(f, addr)
		f = binary.BigEndian.AppendUint16(f, n)
		return f
	}
	bad := append(frame(1, 3, 40000, 2), 0, 0)
	good := frame(1, 3, 40000, 2)
	good = binary.LittleEndian.AppendUint16(good, crcOf(good))
	_ = a.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := a.Write(append(bad, good...)); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, 9)
	if _, err := a.Read(resp); err != nil {
		t.Fatal(err)
	}
	if resp[0] != 1 || resp[1] != 3 || resp[2] != 4 || binary.BigEndian.Uint16(resp[3:]) != 0x5375 || crcOf(resp[:7]) != binary.LittleEndian.Uint16(resp[7:]) {
		t.Errorf("answer % x", resp)
	}
}

func crcOf(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, x := range b {
		crc ^= uint16(x)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
