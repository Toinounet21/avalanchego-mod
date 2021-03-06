// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package snowman

import (
	"github.com/Toinounet21/avalanchego-mod/snow"
	"github.com/Toinounet21/avalanchego-mod/snow/consensus/snowball"
	"github.com/Toinounet21/avalanchego-mod/snow/consensus/snowman"
	"github.com/Toinounet21/avalanchego-mod/snow/engine/common"
	"github.com/Toinounet21/avalanchego-mod/snow/engine/snowman/block"
	"github.com/Toinounet21/avalanchego-mod/snow/validators"
)

// Config wraps all the parameters needed for a snowman engine
type Config struct {
	common.AllGetsServer

	Ctx        *snow.ConsensusContext
	VM         block.ChainVM
	Sender     common.Sender
	Validators validators.Set
	Params     snowball.Parameters
	Consensus  snowman.Consensus
}
