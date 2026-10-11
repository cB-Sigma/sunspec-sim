// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

// DemoOptions shapes the demo inverter.
type DemoOptions struct {
	// Base is the address of the marker (40000, 50000 or 0).
	Base uint16
	// Legacy gives the inverter the older controls model 123 instead of
	// the IEEE 1547-2018 models 704 (output limit, power factor) and 715
	// (start and stop).
	Legacy bool
	// Manufacturer, Model and Serial go into the common model; empty
	// fields get made-up demo values.
	Manufacturer, Model, Serial string
}

// Demo is a demo inverter's image and the addresses of its models' first
// data registers (0 for a model the image does not have).
type Demo struct {
	// Image is the register map; serve it with Start.
	Image *Image
	// Common is model 1, Inverter model 103, Nameplate model 120 and
	// MPPT model 160 (two inputs).
	Common, Inverter, Nameplate, MPPT uint16
	// Controls is model 704, or model 123 when Legacy is set.
	Controls uint16
	// DERCtl is model 715 (absent when Legacy is set).
	DERCtl uint16
}

// DemoInverter builds a 10 kW three-phase inverter: common (1), inverter
// (103), nameplate (120), MPPT with two inputs (160), the controls (704
// and 715, or 123), a vendor model (64900, three registers) and the end
// model. The values are made up; points the demo does not implement hold
// their type's "not implemented" value (NotImplemented). The control
// setpoints and enables are writable.
func DemoInverter(o DemoOptions) Demo {
	if o.Manufacturer == "" {
		o.Manufacturer = "Example Solar"
	}
	if o.Model == "" {
		o.Model = "ES-10K-3P"
	}
	if o.Serial == "" {
		o.Serial = "DEMO000001"
	}
	im := NewImage(o.Base)
	d := Demo{Image: im}

	common := append(append(append(append(append(Str(o.Manufacturer, 16), Str(o.Model, 16)...),
		Str("", 8)...), Str("1.0.0", 8)...), Str(o.Serial, 16)...), 1, 0x8000)
	d.Common = im.Model(1, common)

	inv := NotImplemented(103)
	inv[0], inv[1], inv[2], inv[3], inv[4] = 145, 48, 49, 48, 0xFFFF // A 14.5, phases, A_SF -1
	inv[8], inv[9], inv[10], inv[11] = 2301, 2299, 2304, 0xFFFF      // PhVphA/B/C, V_SF -1
	inv[12], inv[13] = 980, 1                                        // W 9800, W_SF 1
	inv[14], inv[15] = 5001, 0xFFFE                                  // Hz 50.01, Hz_SF -2
	inv[16], inv[18], inv[20] = 0x8000, 0x8000, 0x8000               // VA, VAr, PF not implemented
	inv[22], inv[23], inv[24] = 0x0001, 0x86A0, 0                    // WH 100000, WH_SF 0
	inv[29], inv[30] = 10050, 0                                      // DCW, DCW_SF 0
	inv[31], inv[32], inv[33], inv[34], inv[35] = 452, 0x8000, 0x8000, 0x8000, 0xFFFF
	inv[36] = 4                         // St MPPT
	inv[38], inv[39] = 0, 0             // Evt1: none
	inv[40], inv[41] = 0, 0             // Evt2: none
	inv[42], inv[43], inv[44] = 0, 0, 0 // vendor events: none
	inv[45], inv[46], inv[47], inv[48], inv[49] = 0, 0, 0, 0, 0
	d.Inverter = im.Model(103, inv)

	plate := NotImplemented(120)
	plate[0], plate[1], plate[2] = 4, 10000, 0 // DERTyp PV, WRtg 10000 W, WRtg_SF 0
	d.Nameplate = im.Model(120, plate)

	mppt := NotImplemented(160, 2)
	mppt[0], mppt[1], mppt[2], mppt[3] = 0xFFFE, 0xFFFF, 0, 0 // DCA_SF -2, DCV_SF -1
	mppt[4], mppt[5], mppt[6] = 0, 0, 2                       // Evt, N 2
	for k, m := range [][3]uint16{{835, 6105, 5100}, {812, 6085, 4950}} {
		o := 8 + 20*k
		mppt[o] = uint16(k + 1)
		copy(mppt[o+1:], Str("PV"+string(rune('1'+k)), 8))
		mppt[o+9], mppt[o+10], mppt[o+11] = m[0], m[1], m[2]
		mppt[o+12], mppt[o+13] = 0, 0 // DCWH not accumulated
	}
	d.MPPT = im.Model(160, mppt)

	if o.Legacy {
		ctl := Filled(24, 0)
		ctl[2] = 1                       // Conn CONNECT
		ctl[3], ctl[21] = 1000, 0xFFFF   // WMaxLimPct 100.0 %, SF -1
		ctl[8], ctl[22] = 0x8000, 0xFFFD // OutPFSet not set, SF -3
		ctl[23] = 0x8000                 // VArPct_SF not implemented
		d.Controls = im.Model(123, ctl)
		im.Writable(d.Controls+2, d.Controls+3, d.Controls+7, d.Controls+8, d.Controls+12)
	} else {
		der := NotImplemented(704)
		der[0], der[12], der[13] = 0, 0, 1000 // PFWInjEna off, WMaxLimPctEna off, WMaxLimPct 100.0
		for i := 51; i <= 56; i++ {
			der[i] = 0x8000
		}
		der[51], der[52] = 0xFFFD, 0xFFFF // PF_SF -3, WMaxLimPct_SF -1
		der[57], der[58] = 1000, 0        // PFWInj.PF 1.000, over-excited
		d.Controls = im.Model(704, der)
		im.Writable(d.Controls+0, d.Controls+12, d.Controls+13, d.Controls+57, d.Controls+58)

		ctl := NotImplemented(715)
		ctl[6] = 1 // OpCtl START
		d.DERCtl = im.Model(715, ctl)
		im.Writable(d.DERCtl + 6)
	}
	im.Model(64900, []uint16{1, 2, 3})
	im.End()
	return d
}
