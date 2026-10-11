# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.3.0 (2026-10-11)

Needs github.com/cB-Sigma/sunspec v0.3.0. Backwards compatible with
v0.2.0: the moved names stay in `sim`.

- The image builder and the Modbus serving moved to the sunspec module's
  `device` package; `sim.Image` and `sim.Options` are aliases of its
  types, `Build`, `Example`, `NotImplemented`, `AllModels`, `Str`,
  `Filled`, `NewImage`, `NewDevice`, `Start` and `StartOptions` call it,
  and `sim.Device` embeds a `device.Device` (its methods and hooks) and
  adds the faults, which stay here.
- The device acts on its settings: `Follow` makes the outputs of model
  701 (and 103) follow 704's limit, setpoint and power factor, adopted
  volt-var (705), volt-watt (706), frequency droop (711) and watt-var
  (712) curves, and 715's stop, against a simulated grid (`Grid`,
  `SetGrid`); adopt requests copy the stored curve over the first one
  and set the adopt result (COMPLETED, or FAILED for a curve whose
  points do not rise).
- `DERInverter`: an IEEE 1547-2018 demo inverter (1, 701, 702, 704,
  705, 706, 711, 712, 715) with three stored curves per curve model.
- Modbus ASCII framing over TCP (`Options.ASCII`) and on a byte stream
  (`ServeASCII`).
- `cmd/sunspecsim`: `-models der`, `-grid-v`, `-grid-hz`, `-ascii`.
- Interop: pysunspec2 1.3.6 adopts and enables a volt-var curve and sets
  the output limit on the DER demo; its next scan reads the output
  following them.

## v0.2.0 (2026-10-11)

Needs github.com/cB-Sigma/sunspec v0.2.0. Backwards compatible with
v0.1.0.

- Every model: `Build` lays out any of the 112 described models from
  named raw values, groups nested to any depth included (count points
  set from the lists); `Image.Add` appends it with its writable points;
  `Example` gives made-up values for any model; `AllModels` holds every
  model in one map. `NotImplemented` lays out nested groups through
  `Build` (a group counted by a point now gets that point set).
- Faults: `AddFault` and `ClearFaults` make the device answer with any
  Modbus exception, late, or not at all, by address range, read or write,
  unit and number of requests.
- Transports: `StartOptions` serves Modbus TCP over TLS with client
  certificates and Modbus RTU framing over TCP; `NewDevice` and
  `ServeRTU` serve RTU on any byte stream; `URL` gives the matching
  scheme. The Modbus library's log output is discarded.
- `cmd/sunspecsim`: `-models all` or a list, `-rtu`, `-tls-cert`,
  `-tls-key`, `-tls-ca`.
- Interop: pysunspec2 1.3.6 reads every model from the simulator and
  writes nested curve points to it.

## v0.1.0 (2026-10-11)

First release, split out of the sunspec module (SigmaM2M, a oneM2M
platform). Needs github.com/cB-Sigma/sunspec v0.1.0.

- `sim`: a Modbus TCP server holding a SunSpec register map built model by
  model (`NewImage`, `Model`, `End`, `Writable`, `Str`, `Filled`);
  exception 2 outside the map, writes only on writable registers, a hook
  for a device that keeps another value, unit filtering, a write log;
  `DemoInverter` with the controls models 704 and 715 or 123.
- `NotImplemented`: a described model's registers with every point at its
  type's not-implemented value. The demo inverter now uses it; before, it
  left 0xFFFF in signed points it does not implement, which reads as -1
  (found by pysunspec2).
- `cmd/sunspecsim`: serves the demo inverter.
- The sunspec module's tests and examples that drive the simulator over
  Modbus TCP, and a nested test module `interop` in which pysunspec2 1.3.6
  reads the simulator and writes controls to it.
