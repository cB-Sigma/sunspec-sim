// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package interop holds tests only: pysunspec2, the SunSpec Alliance's
// Python library, written independently of ours and working from its own
// copy of the model definitions, reads the simulated SunSpec devices of
// github.com/cB-Sigma/sunspec-sim over Modbus TCP and writes controls to
// them.
//
// The simulator runs in the test process; the client runs in a pinned
// Python image, which installs the pinned pysunspec2 package from PyPI and
// runs testdata/pysunspec2_client.py, our own script driving pysunspec2's
// client classes. No pysunspec2 code is kept in this repository. The tests
// run only when SIGMA_SUNSPEC_INTEROP=1 and docker is on PATH, and need
// network access for the package:
//
//	cd interop && SIGMA_SUNSPEC_INTEROP=1 GOWORK=off go test -v -timeout 20m ./...
//
// The container reaches the simulator at host.docker.internal. On macOS
// (Docker Desktop) the simulator listens on 127.0.0.1; elsewhere it
// listens on all interfaces for the duration of a test, since a Linux
// container reaches the host through the bridge.
package interop
