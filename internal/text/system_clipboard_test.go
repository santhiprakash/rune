// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package text

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/debug"
)

func TestSystemClipboardDelegatesWhenAvailable(t *testing.T) {
	backing := clipboard.NewInMemory()
	clip := newSystemClipboard(backing, nil)

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"}))
	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.NoError(t, err)
	require.Equal(t, "x", data.Text)
}

func TestSystemClipboardOpenErrorStillFunctionsViaMemory(t *testing.T) {
	clip := newSystemClipboard(nil, errors.New("no provider"))

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: no provider",
		"a failed open must surface through Copy with a user-friendly message")

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, "system clipboard: no provider",
		"a failed open must surface through Paste with a user-friendly message")
	require.Equal(t, "x", data.Text,
		"the in-memory fallback must still service copy/paste within Rune")
}

func TestSystemClipboardUnsupportedErrorSuggestsPackages(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("package-install hint is only appended on Linux")
	}
	clip := newSystemClipboard(nil, errors.New("system clipboard unsupported"))

	want := "system clipboard: system clipboard unsupported; " +
		"install one of the following clipboard utilities: " +
		"xclip, xsel, or wl-clipboard (Wayland)"

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, want,
		"an unsupported clipboard must tell the user which packages to install")

	_, err = clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, want,
		"an unsupported clipboard must tell the user which packages to install")
}

func TestSystemClipboardUnsupportedErrorNoHintOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("hint is expected on Linux")
	}
	clip := newSystemClipboard(nil, errors.New("system clipboard unsupported"))

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: system clipboard unsupported",
		"non-Linux platforms must not get Linux package advice")
}

func TestSystemClipboardCopyPasteErrorStillFunctionsViaMemory(t *testing.T) {
	sysErr := errors.New("write failed")
	clip := newSystemClipboard(failingRegister{err: sysErr}, nil)

	err := clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "x"})
	require.EqualError(t, err, "system clipboard: write failed",
		"a failed system Copy must surface a user-friendly error")

	data, err := clip.Paste(clipboard.DefaultRegisterID)
	require.EqualError(t, err, "system clipboard: write failed",
		"a failed system Paste must surface a user-friendly error")
	require.Equal(t, "x", data.Text,
		"the in-memory fallback must still service copy/paste within Rune")
}

type failingRegister struct {
	err error
}

func (f failingRegister) Copy(string, clipboard.Data) error { return f.err }

func (f failingRegister) Paste(string) (clipboard.Data, error) {
	return clipboard.Data{}, f.err
}

// recordingRegister records every register ID written to the underlying OS
// clipboard so tests can assert what is forwarded to the system layer.
type recordingRegister struct {
	clipboard.Register
	copied []string
}

func (r *recordingRegister) Copy(registerID string, data clipboard.Data) error {
	r.copied = append(r.copied, registerID)
	return r.Register.Copy(registerID, data)
}

// TestSystemClipboardOnlyDefaultRegisterReachesOS reproduces the bug where
// typing in the modal compose editor leaked to the OS clipboard: the vi
// editor writes the typed run to its "." register on insert-mode exit, and
// the system clipboard forwarded every register to the OS. Only the default
// register represents the single OS clipboard; all other registers must stay
// in the in-memory shadow.
func TestSystemClipboardOnlyDefaultRegisterReachesOS(t *testing.T) {
	sys := &recordingRegister{Register: clipboard.NewInMemory()}
	clip := newSystemClipboard(sys, nil)

	require.NoError(t, clip.Copy(".", clipboard.Data{Text: "typed text"}))
	require.Empty(t, sys.copied,
		"a non-default register write must not reach the OS clipboard")

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "yanked"}))
	require.Equal(t, []string{clipboard.DefaultRegisterID}, sys.copied,
		"only the default register write may reach the OS clipboard")
}

// burstRegister holds its first system Copy on a gate so a burst of copies
// overlaps one in-flight OS write, the way wl-copy latency stretches a Vim
// `.` repeat. It counts what actually reaches the OS layer.
type burstRegister struct {
	clipboard.Register
	entered chan struct{}
	gate    chan struct{}

	mu    sync.Mutex
	calls int
	last  clipboard.Data
}

func (r *burstRegister) Copy(registerID string, data clipboard.Data) error {
	r.mu.Lock()
	r.calls++
	r.last = data
	first := r.calls == 1
	r.mu.Unlock()
	if first {
		close(r.entered)
		<-r.gate
	}
	return r.Register.Copy(registerID, data)
}

// TestSystemClipboardCoalescesBurstCopies reproduces the hang from holding `.`
// to repeat a Vim dw: every repeated delete re-copied the deleted word and
// each copy shelled out a fresh wl-copy, flooding Wayland with subprocesses.
// Copies landing while an OS write is in flight must coalesce so the newest
// payload — not every payload — is forwarded.
func TestSystemClipboardCoalescesBurstCopies(t *testing.T) {
	sys := &burstRegister{
		Register: clipboard.NewInMemory(),
		entered:  make(chan struct{}),
		gate:     make(chan struct{}),
	}
	clip := newSystemClipboard(sys, nil)

	first := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		first <- clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "word-0"})
	})
	<-sys.entered

	const burst = 50
	for i := 1; i <= burst; i++ {
		require.NoError(t, clip.Copy(
			clipboard.DefaultRegisterID, clipboard.Data{Text: fmt.Sprintf("word-%d", i)}))
	}
	close(sys.gate)

	select {
	case err := <-first:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("first copy did not return")
	}

	require.Eventually(t, func() bool {
		sys.mu.Lock()
		defer sys.mu.Unlock()
		return sys.calls == 2
	}, 5*time.Second, time.Millisecond,
		"one in-flight copy plus one coalesced drain must replace a subprocess per repeat")

	sys.mu.Lock()
	require.Equal(t, fmt.Sprintf("word-%d", burst), sys.last.Text,
		"the OS clipboard must end up holding the newest payload, not an intermediate")
	sys.mu.Unlock()

	require.NoError(t, clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: "next"}))
	sys.mu.Lock()
	require.Equal(t, 3, sys.calls,
		"a copy that arrives after the burst settles must still write through")
	sys.mu.Unlock()
}
