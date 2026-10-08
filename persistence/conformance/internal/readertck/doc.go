// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Package readertck is the specification harness for the journal READER
// conformance suite (issue #348, I-02): a deterministic scenario runner, an
// independent oracle and a ground-truth log. It checks three separate
// properties of any reader mechanism: safety, eligibility and progress. See
// docs/testing/reader-conformance.md for their definitions and for the
// necessary conditions of each.
//
// # Status: provisional, internal, mechanism-neutral
//
// No reader SPI is approved yet (#351 specifies the contract, #347 is not
// merged). Everything here is written against the small test-local interfaces
// Backend and Subject. They are NOT a proposal for the public API: names,
// shapes and the opaque []byte cursor are placeholders. The package is
// internal to persistence/conformance on purpose, so nothing is exported to
// adapter authors until the maintainers approve a contract.
//
// The package does not choose, favour or implement a read mechanism (commit
// position, transaction-id horizon, set of delivered keys, ...). Choosing is
// #352 / Gate A. The fakes in the tests include two different correct
// mechanisms so that the harness cannot be mistaken for either of them.
//
// # Independence of the oracle
//
// The oracle never reads a cursor. The cursor is an opaque byte slice that the
// runner stores and hands back. What a correct reader must deliver is computed
// only from Truth, the ground-truth log fed by the scenario script as it
// drives the backend: appends, commits, aborts and the logical clock.
//
// # Determinism
//
// There is no wall clock and no sleep. Time is a logical tick counter moved by
// the script. Randomised scenarios come from Generate(seed) and are plain data.
package readertck
