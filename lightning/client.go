package lightning

import (
	"github.com/breez/lspd/basetypes"
	"github.com/btcsuite/btcd/wire"
)

type GetInfoResult struct {
	Alias  string
	Pubkey string
}

type GetChannelResult struct {
	InitialChannelID   basetypes.ShortChannelID
	ConfirmedChannelID basetypes.ShortChannelID
}

type OpenChannelRequest struct {
	Destination    []byte
	CapacitySat    uint64
	MinHtlcMsat    uint64
	IsPrivate      bool
	IsZeroConf     bool
	MinConfs       *uint32
	FeeSatPerVByte *float64
	TargetConf     *uint32
}

type Client interface {
	GetInfo() (*GetInfoResult, error)
	IsConnected(destination []byte) (bool, error)
	OpenChannel(req *OpenChannelRequest) (*wire.OutPoint, error)
	GetChannel(peerID []byte, channelPoint wire.OutPoint) (*GetChannelResult, error)
	GetNodeChannelCount(nodeID []byte) (int, error)
	GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error)
}
