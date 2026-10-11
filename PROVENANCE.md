# Provenance

## How the code was written

Written in Go for SigmaM2M, a oneM2M platform, as the test device of the
sunspec module. No code was ported or copied from another implementation.

- `sim/sim.go` was written inside SigmaM2M and moved into the sunspec
  module on 2026-10-06, when it lost its dependency on package testing
  (`Start`, `Close`, `Addr`, `URL`, `SetKeep` and `WriteLog` replaced the
  earlier test helpers and exported fields); `sim/demo.go`,
  `cmd/sunspecsim` and the tests were written for that module.
- On 2026-10-11 the simulator, its command and the tests that drive the
  simulator moved out of the sunspec module into this one, so that the
  library does not depend on its simulator. `sunspec_test.go` and
  `example_test.go` at the module root are the sunspec module's map,
  control and example tests over Modbus TCP against the simulator.
- Also on 2026-10-11, pysunspec2 reading the demo inverter showed that the
  demo filled every point it does not implement with 0xFFFF, which is a
  value (-1) for the signed types, so pysunspec2 read model 704's WSet as
  -1. `sim/notimpl.go` (`NotImplemented`) now fills each point with its
  type's not-implemented value from the sunspec tables, the demo uses it,
  and `TestNotImplemented` and `TestDemoNotImplementedPoints` guard it.

## Sources

| Area (files) | Followed |
|---|---|
| Register map layout (sim/sim.go, sim/demo.go) | SunSpec Device Information Model Specification v1.2, section 6 (the marker, base addresses, model header, end model) |
| Point layout and not-implemented values (sim/demo.go, sim/notimpl.go) | The SunSpec model definitions, through the tables of github.com/cB-Sigma/sunspec (see NOTICE) |
| Exceptions (sim/sim.go) | The Modbus application protocol's exception codes 1, 2 and 11 |
| Modbus TCP server | github.com/simonvetter/modbus |

No specification text is included; comments cite sections in our own
words.

## Test data

- `sim.DemoInverter` and `cmd/sunspecsim`: made-up identity ("Example
  Solar", "ES-10K-3P", "DEMO000001") and values.
- `sunspec_test.go`: an inverter image laid out by hand from the model
  definitions, using a real manufacturer's name and model name as text
  only; the serial number and register values are made up.
- `sim/` tests, the runner's tests and `interop/`: values written for the
  tests; the made-up models numbered 64000 and above are not SunSpec
  models.

## Requirement IDs

Tests carry `// Covers: SIG-...` comments. These are requirement IDs of
the SigmaM2M project's compliance report; they mean nothing else here.
