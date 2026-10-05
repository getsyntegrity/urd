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

package tenancy_test

import (
	"context"
	"os"
	"testing"

	pginfra "github.com/getsyntegrity/urd/inttest/infra/postgres"
)

// shared is the one Postgres container of this package. TestMain starts it before the first test and terminates
// it after the last one. Tests never read it before m.Run, and they only call NewDatabase on it.
var shared *pginfra.Postgres

func TestMain(m *testing.M) { os.Exit(run(m)) }

// run exists so the container is terminated by a defer even when m.Run panics, before os.Exit ends the process.
func run(m *testing.M) int {
	ctx := context.Background()
	pg, err := pginfra.StartPostgres(ctx)
	if err != nil {
		_, _ = os.Stderr.WriteString("inttest/flows/tenancy: cannot start the Postgres container, the tests cannot run without it: " + err.Error() + "\n")
		return 1
	}
	defer func() {
		if err := pg.Terminate(ctx); err != nil {
			_, _ = os.Stderr.WriteString("inttest/flows/tenancy: cannot terminate the Postgres container: " + err.Error() + "\n")
		}
	}()
	shared = pg
	return m.Run()
}
