// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package network

import (
	"time"

	"github.com/Toinounet21/avalanchego-mod/ids"
	"github.com/Toinounet21/avalanchego-mod/utils/json"
)

type PeerInfo struct {
	IP             string     `json:"ip"`
	PublicIP       string     `json:"publicIP,omitempty"`
	ID             string     `json:"nodeID"`
	Version        string     `json:"version"`
	LastSent       time.Time  `json:"lastSent"`
	LastReceived   time.Time  `json:"lastReceived"`
	Benched        []ids.ID   `json:"benched"`
	ObservedUptime json.Uint8 `json:"observedUptime"`
}
