# sunspec-sim

A simulated SunSpec device over Modbus in Go, for testing SunSpec
clients without an inverter.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/sunspec-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/sunspec-sim) [![test](https://github.com/cB-Sigma/sunspec-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/sunspec-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.22-00ADD8.svg)](go.mod)

The simulator serves a SunSpec register map laid out register by
register, as an inverter publishes it: the "SunS" marker at 40000, 50000
or 0, then the models and the end model. It lays out any of the 112
SunSpec models from named values, groups nested to any depth included
(the curves of models 705 to 712), and can hold all of them at once. It
speaks Modbus TCP, Modbus TCP over TLS with client certificates, and
Modbus RTU or ASCII framing over TCP or over any byte stream (a serial
port you open). It refuses reads outside the map and writes to registers
that are not writable with Modbus exception 2, answers only the units it
is given, can keep a different value from the one written (an inverter
that clamps a setpoint), logs every write, and misbehaves on request:
exceptions, late answers, no answer. An IEEE 1547-2018 inverter acts on
its settings: its output follows the output limit, power factor and the
volt-var, volt-watt, frequency droop and watt-var curves it adopts,
against a simulated grid voltage and frequency. A ready-made 10 kW demo
inverter has the common, inverter, nameplate, MPPT and DER control
models. It runs in-process or from the command line, and is built on
[sunspec](https://github.com/cB-Sigma/sunspec)'s model tables and its
`device` package (which lays out and serves the map; since v0.3.0 the
image builder and the server live there, and `sim` keeps its names for
them). Written for SigmaM2M, a oneM2M platform; it has no dependency on
it.

## What is simulated

| Package | What it does |
|---|---|
| `sim` | `NewImage(base)`, then `Model(id, data)` per model (or `Add(id, values)`, which lays the model out with `Build` and marks its writable points) and `End`; `Writable` marks registers that take writes; `Build` (named raw values, nested groups as lists of maps, count points filled in), `Example` (made-up values for any model), `NotImplemented`, `Str` and `Filled` build model data; `AllModels` holds every described model; `Start(image, listen, units...)` serves Modbus TCP, `StartOptions` also TLS and RTU framing over TCP, `NewDevice` and `ServeRTU` serve RTU on any byte stream (`Addr`, `URL`, `Close`); `Get` and `Set` read and change registers as the device would, `SetKeep` makes it keep another value than the one written, `AddFault` and `ClearFaults` make it answer with an exception, late or not at all (by address range, read or write, unit, number of requests), `WriteLog` lists the writes; `Options.ASCII` and `ServeASCII` serve Modbus ASCII. `DemoInverter` builds the demo; `DERInverter` an IEEE 1547-2018 inverter (1, 701, 702, 704, 705, 706, 711, 712, 715); `Follow` makes its outputs follow its settings against a simulated `Grid` (`SetGrid`, `Output`). The image, builder and server are the sunspec module's `device` package, under the names they had here |
| `cmd/sunspecsim` | Serves the demo inverter, every model, or the models listed |

The demo inverter (`DemoInverter`): common model 1, three-phase inverter
103, nameplate 120, MPPT 160 with two inputs, the IEEE 1547-2018 DER
controls 704 and 715 (or the older controls model 123 with `Legacy`), a
vendor model 64900 and the end model. The control setpoints and enables
are writable; every point the demo does not implement holds its
not-implemented value.

With `Follow` (see `DERInverter`, `-models der`), the device acts on its
settings: an adopt request (705 to 712) checks the stored curve (it
exists, its active points fit, its x values rise), copies it over the
first curve and sets the adopt result (COMPLETED or FAILED); model 701
(and 103, when present) then shows the active power capped by 704's
limit and setpoint, volt-watt and over-frequency droop, and the reactive
power from 704's power factor or setpoint, else volt-var, else
watt-var; 715's STOP stops the output.

Not simulated: Modbus over UDP; response times, ramps and reversion
timers (changes are immediate); the trip curves 707 to 710 (adopted, not
acted on); under-frequency response (it cannot add power above what is
available); the curves' read-only flag (writes follow the definitions'
access only, as pysunspec2's own device does); the demo inverter
(`DemoInverter`, models 103 and 704) does not follow its settings, only
maps that have model 701 do.

## Install

```
go get github.com/cB-Sigma/sunspec-sim@latest
```

Requires Go 1.22 or later. The command:

```
go install github.com/cB-Sigma/sunspec-sim/cmd/sunspecsim@latest
```

## Command line

```
sunspecsim -listen 127.0.0.1:5020 -unit 1 -base 40000 -vary 5s
sunspecsim -listen 127.0.0.1:5020 -unit 126 -base 50000 -legacy
sunspecsim -listen 127.0.0.1:5020 -models all
sunspecsim -listen 127.0.0.1:4001 -models 1,705,707 -rtu
sunspecsim -listen 127.0.0.1:5020 -models der -grid-v 241.5 -grid-hz 50.2
sunspecsim -listen 127.0.0.1:4002 -ascii
sunspecsim -listen 127.0.0.1:802 -tls-cert server.pem -tls-key server.key -tls-ca ca.pem
```

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:5020` | address to serve on (port 0: a free port) |
| `-unit` | `0` | the only unit to answer, 1 to 247 (0: every unit) |
| `-base` | `40000` | address of the SunSpec marker: 40000, 50000 or 0 |
| `-legacy` | off | the controls model 123 instead of 704 and 715 |
| `-vary` | `0` | change the demo's output power at this interval (0: never) |
| `-models` | `demo` | `demo`, `der` (an IEEE 1547-2018 inverter following its settings), `all` (every described model with made-up values), or model IDs (`1,705,707`) |
| `-grid-v`, `-grid-hz` | `0` | with `der`: the simulated grid voltage and frequency (0: 230 V, 50 Hz) |
| `-rtu` | off | Modbus RTU framing over TCP (as a serial gateway) instead of Modbus TCP |
| `-ascii` | off | Modbus ASCII framing over TCP instead of Modbus TCP |
| `-tls-cert`, `-tls-key`, `-tls-ca` | none | serve Modbus TCP over TLS; client certificates must chain to the CA |

## In Go tests

From `Example` in example_test.go:

```go
demo := sim.DemoInverter(sim.DemoOptions{Base: 40000})
dev, err := sim.Start(demo.Image, "127.0.0.1:0")
defer dev.Close()

r, err := mbtcp.Dial(dev.URL(), 1, 2*time.Second)
m, err := sunspec.Find(r) // models 1, 103, 120, 160, 704, 715, 64900
```

A map of your own, with a volt-var curve set:

```go
im := sim.NewImage(40000)
im.Add(1, map[string]any{"Mn": "Example Solar", "Md": "ES-10K-3P", "SN": "X1"})
im.Add(705, map[string]any{"V_SF": -1, "Crv": []map[string]any{
	{"ActPt": 2, "ReadOnly": 1, "Pt": []map[string]any{{"V": 950, "Var": 30}, {"V": 1050, "Var": -30}}},
	{"ActPt": 2, "Pt": []map[string]any{{}, {}}},
}}) // NPt and NCrv set to 2; curve points writable
im.End()
dev, err := sim.Start(im, "", 1) // unit 1 only
dev.SetKeep(func(addr, v uint16) uint16 { return min(v, 900) })
dev.AddFault(sim.Fault{Op: "write", Exception: 6, Times: 1}) // the next write: busy
```

An inverter whose output follows its settings (`TestFollowCurves`):

```go
der := sim.DERInverter(sim.DEROptions{Base: 40000})
dev, err := sim.Start(der.Image, "")
f, err := sim.Follow(dev, sim.FollowOptions{Grid: sim.Grid{V: 230, Hz: 50, Avail: 10000}})
// a client adopts and enables a volt-var curve (sunspec.PlanCurve) ...
f.SetGrid(sim.Grid{V: 241.5, Hz: 50, Avail: 10000}) // 105 %: 701.Var follows the curve
```

## Documentation

Reference: https://pkg.go.dev/github.com/cB-Sigma/sunspec-sim

## Testing

```
go vet ./...
go test -race ./...
```

The tests at the module root are the sunspec module's map, control and
example tests over Modbus TCP against the simulator; `sim` and
`cmd/sunspecsim` test the simulator and the command (every model over
Modbus TCP, nested groups, faults, TLS with generated certificates, RTU
framing over TCP and on a byte stream). The simulator and the sunspec
reader are written by the same author from the same documents, so these
tests show the two agree; the independent check is below.

`interop/` is a nested module with its own go.mod. Its tests run
pysunspec2 in Docker and are skipped unless `SIGMA_SUNSPEC_INTEROP=1` is
set; they need network access for the image and the package:

```
cd interop && SIGMA_SUNSPEC_INTEROP=1 go test -v ./...
```

### Interop tested against

- pysunspec2 1.3.6 (the SunSpec Alliance's Python library, Apache-2.0,
  from PyPI) in python:3.12.10-slim on Docker Desktop for macOS (arm64),
  2026-10-11, reading the simulator over Modbus TCP from the container
  (`interop`):
  - every model at once (`AllModels`, two occurrences of every repeating
    group, nested ones included): pysunspec2's scan found all 112 models
    at the simulator's addresses and lengths with no model error, and it
    and the sunspec module agreed on every point's place and on 4286
    values (196 in groups nested two or three deep);
  - curve writes by pysunspec2 to models 705 and 707, by nested path
    ("Crv.2.Pt.1.V", "Crv.2.MustTrip.Pt.2.Tms"), with the adopt request
    and the enable: the simulator kept each; a write to a curve's
    read-only flag was refused;
  - the demo inverter at 40000: pysunspec2's scan found the chain (1, 103,
    120, 160, 704, 715 and the vendor model 64900) with no model errors,
    and agreed on 37 values and on 10 points the demo does not implement;
    its writes of the output limit, power factor, excitation and stop
    command landed; its write to the read-only inverter power was
    refused;
  - the legacy demo (model 123) at 50000 answering unit 126 only, and a
    map at address 0 with models 101, 111, 201, 701, 713 and 122: found,
    decoded and written as expected.
- pysunspec2 1.3.6 driving the DER inverter, 2026-10-12
  (`interop/follow_test.go`): it wrote a curve point, the adopt request
  and the enable of volt-var and the output limit on 704; its next scan
  read model 701 following them at the simulated 241.5 V (6000 W, -880
  var), the adopt result COMPLETED and the request back at 0.
- Found on the way and fixed in v0.1.0: the demo left 0xFFFF in signed
  points it did not implement, which pysunspec2 rightly read as -1 (see
  CHANGELOG.md).
- TLS and RTU: read by the sunspec module's `mbtcp` (simonvetter/modbus
  clients); not by pysunspec2, whose RTU client needs a serial port.
  ASCII: the serving code is the sunspec module's `device` package, which
  that module checks against pymodbus 3.8.6 (pysunspec2 has no ASCII
  client).
- Not yet: a commercial SunSpec test tool, a real inverter's firmware
  quirks.

## Related repositories

- [sunspec](https://github.com/cB-Sigma/sunspec): the SunSpec reader,
  writes, DER controls and curves the simulator is built for.

## Status

v0.x. The API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE)). Copyright 2026 Abhinav Gautam.
[NOTICE](NOTICE) names the dependencies, the source of the model
definitions and the trademarks used.
