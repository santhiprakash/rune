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

	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/clipboard/sysclip"
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
	if err := c.sys.Copy(registerID, data); err != nil {
		return fmt.Errorf("system clipboard: %s", err.Error())
	}
	return nil
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
