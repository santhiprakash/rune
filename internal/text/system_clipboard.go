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
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/clipboard/sysclip"
	"unstable.build/rune/internal/debug"
)

// NewSystemClipboard returns a clipboard.Register backed by the OS
// clipboard.
func NewSystemClipboard() clipboard.Register {
	return newSystemClipboard(sysclip.NewRegister())
}

type systemClipboard struct {
	sys     clipboard.Register
	mem     clipboard.Register
	openErr error

	mu      sync.Mutex
	writing bool
	queued  bool
	pending clipboard.Data
}

func newSystemClipboard(sys clipboard.Register, err error) clipboard.Register {
	if err != nil {
		msg := err.Error()
		if runtime.GOOS == "linux" && strings.Contains(msg, "unsupported") {
			msg += "; install one of the following clipboard utilities: " +
				"xclip, xsel, or wl-clipboard (Wayland)"
		}
		err = fmt.Errorf("system clipboard: %s", msg)
	}
	return &systemClipboard{sys: sys, mem: clipboard.NewInMemory(), openErr: err}
}

func (c *systemClipboard) Copy(registerID string, data clipboard.Data) error {
	_ = c.mem.Copy(registerID, data)
	if registerID != clipboard.DefaultRegisterID {
		return nil
	}
	if c.openErr != nil {
		return c.openErr
	}

	c.mu.Lock()
	if c.writing {
		// The OS backend shells out once per call (wl-copy on Wayland), so a
		// burst such as a held `.` repeating a Vim delete must not forward
		// every payload: only the newest one still matters once the in-flight
		// write finishes.
		c.pending = data
		c.queued = true
		c.mu.Unlock()
		return nil
	}
	c.writing = true
	c.mu.Unlock()

	err := c.sys.Copy(registerID, data)

	c.mu.Lock()
	if c.queued {
		go debug.CapturePanicReport(c.drain)
	} else {
		c.writing = false
	}
	c.mu.Unlock()

	if err != nil {
		return fmt.Errorf("system clipboard: %s", err.Error())
	}
	return nil
}

// drain forwards queued payloads to the OS clipboard until the burst quiets.
// It inherits the writer role (writing stays set) so copies arriving during a
// slow subprocess keep coalescing instead of spawning one of their own. Errors
// are dropped: callers ignore Copy errors and the next settled write still
// surfaces a broken OS clipboard.
func (c *systemClipboard) drain() {
	for {
		c.mu.Lock()
		if !c.queued {
			c.writing = false
			c.mu.Unlock()
			return
		}
		data := c.pending
		c.queued = false
		c.mu.Unlock()
		_ = c.sys.Copy(clipboard.DefaultRegisterID, data)
	}
}

func (c *systemClipboard) Paste(registerID string) (clipboard.Data, error) {
	if c.openErr != nil {
		data, _ := c.mem.Paste(registerID)
		return data, c.openErr
	}
	data, err := c.sys.Paste(registerID)
	if err != nil {
		memData, _ := c.mem.Paste(registerID)
		return memData, fmt.Errorf("system clipboard: %s", err.Error())
	}
	return data, nil
}
