# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

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
